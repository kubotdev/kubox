package env

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/kubotdev/kubox/internal/slice"
)

const (
	ReportPass  = "pass"
	ReportFail  = "fail"
	ReportError = "error"
)

// Check types.
const (
	CheckTypeLoad            = "load"
	CheckTypeDrillDepRestart = "drill:dependency-restart"
)

type CheckStatus string

const (
	CheckPass    CheckStatus = "pass"
	CheckFail    CheckStatus = "fail"
	CheckError   CheckStatus = "error"
	CheckSkipped CheckStatus = "skipped"
)

type Finding struct {
	Metric   string `json:"metric,omitempty"`
	Expected string `json:"expected,omitempty"`
	Actual   string `json:"actual,omitempty"`
	Message  string `json:"message,omitempty"`
}

type Check struct {
	Type       string          `json:"type"`
	Status     CheckStatus     `json:"status"`
	DurationMS int64           `json:"duration_ms,omitempty"`
	Findings   []Finding       `json:"findings,omitempty"`
	Detail     json.RawMessage `json:"detail,omitempty"`
}

type EnvProblem struct {
	Component string `json:"component,omitempty"`
	Kind      string `json:"kind"`
	Message   string `json:"message,omitempty"`
}

type EnvHealth struct {
	Healthy   bool         `json:"healthy"`
	Namespace string       `json:"namespace"`
	Problems  []EnvProblem `json:"problems,omitempty"`
}

func (h *EnvHealth) add(p EnvProblem) {
	h.Healthy = false
	h.Problems = append(h.Problems, p)
}

type Report struct {
	Status      string    `json:"status"` // pass | fail | error
	Environment EnvHealth `json:"environment"`
	Checks      []Check   `json:"checks"`
}

func NewReport(h EnvHealth, checks ...Check) *Report {
	r := &Report{Environment: h, Checks: checks}
	r.finalize()
	return r
}

func (r *Report) finalize() {
	r.Status = ReportPass
	if !r.Environment.Healthy {
		r.Status = ReportError
		return
	}
	for _, c := range r.Checks {
		switch c.Status {
		case CheckError:
			r.Status = ReportError
		case CheckFail:
			if r.Status == ReportPass {
				r.Status = ReportFail
			}
		}
	}
}

var containerFailureReasons = map[string]bool{
	"CrashLoopBackOff":           true,
	"ErrImagePull":               true,
	"ImagePullBackOff":           true,
	"InvalidImageName":           true,
	"CreateContainerConfigError": true,
	"CreateContainerError":       true,
	"RunContainerError":          true,
}

var transientComponents = map[string]bool{
	"k6":     true,
	"runner": true,
}

func (m *Manager) CheckEnv(ctx context.Context, envName string) (*EnvHealth, error) {
	ns := slice.EnvNamespace(envName)
	h := &EnvHealth{Healthy: true, Namespace: ns}

	if _, err := m.Client.CoreV1().Namespaces().Get(ctx, ns, metav1.GetOptions{}); err != nil {
		if apierrors.IsNotFound(err) {
			h.add(EnvProblem{
				Kind:    "NotFound",
				Message: fmt.Sprintf("environment %q not found — run `kubox up` first", envName),
			})
			return h, nil
		}
		return nil, err
	}

	// Container-level failures and unschedulable pods.
	failedComponents := map[string]bool{}
	pods, err := m.Client.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{
		LabelSelector: LabelEnv + "=" + envName,
	})
	if err != nil {
		return nil, fmt.Errorf("list pods: %w", err)
	}
	for i := range pods.Items {
		p := &pods.Items[i]
		component := p.Labels[LabelComponent]
		if transientComponents[component] {
			continue
		}
		if reported := podFailure(p); reported != nil {
			reported.Component = component
			h.add(*reported)
			failedComponents[component] = true
		}
	}

	// Deployment readiness (skip components with a root cause above).
	deployments, err := m.Client.AppsV1().Deployments(ns).List(ctx, metav1.ListOptions{
		LabelSelector: LabelManagedBy + "=" + ManagedByValue,
	})
	if err != nil {
		return nil, fmt.Errorf("list deployments: %w", err)
	}
	for i := range deployments.Items {
		d := &deployments.Items[i]
		if failedComponents[d.Name] {
			continue
		}
		want := int32(1)
		if d.Spec.Replicas != nil {
			want = *d.Spec.Replicas
		}
		if d.Status.AvailableReplicas < want {
			h.add(EnvProblem{
				Component: d.Name,
				Kind:      "NotReady",
				Message: fmt.Sprintf("deployment %s: %d/%d replicas available",
					d.Name, d.Status.AvailableReplicas, want),
			})
		}
	}

	// The app service exists and has ready endpoints.
	svc, err := m.Client.CoreV1().Services(ns).Get(ctx, envName, metav1.GetOptions{})
	switch {
	case apierrors.IsNotFound(err):
		if !failedComponents[envName] {
			h.add(EnvProblem{
				Component: envName,
				Kind:      "NoService",
				Message:   fmt.Sprintf("app service %q not found in %s", envName, ns),
			})
		}
	case err != nil:
		return nil, fmt.Errorf("get app service: %w", err)
	default:
		if len(svc.Spec.Ports) > 0 && !failedComponents[envName] {
			ready := 0
			if eps, err := m.Client.CoreV1().Endpoints(ns).Get(ctx, envName, metav1.GetOptions{}); err == nil {
				for _, sub := range eps.Subsets {
					ready += len(sub.Addresses)
				}
			} else if !apierrors.IsNotFound(err) {
				return nil, fmt.Errorf("get app endpoints: %w", err)
			}
			if ready == 0 {
				h.add(EnvProblem{
					Component: envName,
					Kind:      "NoEndpoints",
					Message:   fmt.Sprintf("app service %q has no ready endpoints", envName),
				})
			}
		}
	}
	return h, nil
}

func podFailure(p *corev1.Pod) *EnvProblem {
	for _, cs := range p.Status.ContainerStatuses {
		if w := cs.State.Waiting; w != nil && containerFailureReasons[w.Reason] {
			msg := w.Message
			if msg == "" {
				msg = w.Reason
			}
			return &EnvProblem{
				Kind:    w.Reason,
				Message: fmt.Sprintf("pod %s: %s", p.Name, msg),
			}
		}
	}
	if p.Status.Phase == corev1.PodPending {
		for _, c := range p.Status.Conditions {
			if c.Type == corev1.PodScheduled && c.Status == corev1.ConditionFalse &&
				c.Reason == "Unschedulable" && c.Message != "" {
				kind := "Unschedulable"
				if strings.Contains(c.Message, "exceeded quota") {
					kind = "FailedQuota"
				}
				return &EnvProblem{
					Kind:    kind,
					Message: fmt.Sprintf("pod %s: %s", p.Name, c.Message),
				}
			}
		}
	}
	return nil
}

func LoadCheck(r *K6Result, d time.Duration) Check {
	c := Check{
		Type:       CheckTypeLoad,
		Status:     CheckPass,
		DurationMS: d.Milliseconds(),
	}
	if r.Status != "pass" {
		c.Status = CheckFail
	}
	names := make([]string, 0, len(r.Thresholds))
	for name := range r.Thresholds {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		t := r.Thresholds[name]
		if t.Pass {
			continue
		}
		actual := fmt.Sprintf("%g", t.Value)
		c.Findings = append(c.Findings, Finding{
			Metric:   name,
			Expected: "pass",
			Actual:   actual,
			Message:  fmt.Sprintf("threshold %s not met (value %s)", name, actual),
		})
	}
	if c.Status == CheckFail && len(c.Findings) == 0 {
		c.Findings = append(c.Findings, Finding{
			Message: "k6 exited nonzero but no thresholds were crossed (script error?)",
		})
	}
	detail, err := json.Marshal(r)
	if err == nil {
		c.Detail = detail
	}
	return c
}
