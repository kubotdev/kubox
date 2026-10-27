package env

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/kubotdev/kubox/internal/slice"
)

const (
	DrillDependencyRestart = "dependency-restart"
	defaultDrillTimeout    = 2 * time.Minute
	drillPollInterval      = 2 * time.Second
)

type DrillSpec struct {
	Type       string
	Dependency string
	Timeout    time.Duration
}

func (s *DrillSpec) normalize() {
	if s.Timeout <= 0 {
		s.Timeout = defaultDrillTimeout
	}
}

type DrillPhase struct {
	Name       string `json:"name"`
	Status     string `json:"status"` // pass | timeout | failed | skipped
	DurationMS int64  `json:"duration_ms,omitempty"`
	Message    string `json:"message,omitempty"`
}

type DrillDetail struct {
	Target string       `json:"target"`
	Fault  string       `json:"fault"`
	Phases []DrillPhase `json:"phases"`
}

type DrillResult struct {
	Check       Check
	Environment EnvHealth
}

func (m *Manager) RunDrill(ctx context.Context, envName string, spec DrillSpec) (*DrillResult, error) {
	spec.normalize()
	if spec.Type != DrillDependencyRestart {
		return nil, fmt.Errorf("unknown drill type %q (supported: %s)", spec.Type, DrillDependencyRestart)
	}

	health, err := m.CheckEnv(ctx, envName)
	if err != nil {
		return nil, err
	}
	if !health.Healthy {
		return &DrillResult{Environment: *health}, nil
	}

	if spec.Dependency == "" {
		return nil, fmt.Errorf("%s requires a target dependency (--dependency)", spec.Type)
	}
	if spec.Dependency == envName {
		return nil, fmt.Errorf(
			"target %q is the app itself — restart it with `kubox exec %s -- kubectl delete pod -l app=%s`",
			spec.Dependency, envName, envName)
	}

	ns := slice.EnvNamespace(envName)
	svc, err := m.Client.CoreV1().Services(ns).Get(ctx, spec.Dependency, metav1.GetOptions{})
	if err != nil || svc.Labels[LabelEnv] != envName {
		return nil, fmt.Errorf("dependency %q is not a service of environment %q", spec.Dependency, envName)
	}

	check, err := m.runDependencyRestart(ctx, envName, ns, spec)
	if err != nil {
		return nil, err
	}
	return &DrillResult{Check: check, Environment: *health}, nil
}

func (m *Manager) runDependencyRestart(ctx context.Context, envName, ns string, spec DrillSpec) (Check, error) {
	target := spec.Dependency
	start := time.Now()

	appPort, err := m.appPort(ctx, ns, envName)
	if err != nil {
		return Check{}, err
	}
	healthPath := m.appHealthPath(ctx, ns)

	snapshot, err := m.podUIDs(ctx, ns, target)
	if err != nil {
		return Check{}, err
	}

	detail := DrillDetail{Target: target, Fault: "delete-pods"}
	var phases []DrillPhase
	ok := true
	record := func(name string, fn func() (string, error)) {
		if !ok {
			phases = append(phases, DrillPhase{Name: name, Status: "skipped"})
			return
		}
		t0 := time.Now()
		msg, err := fn()
		ph := DrillPhase{
			Name:       name,
			Status:     "pass",
			DurationMS: time.Since(t0).Milliseconds(),
			Message:    msg,
		}
		if err != nil {
			if errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
				ph.Status = "timeout"
			} else {
				ph.Status = "failed"
			}
			if msg == "" {
				ph.Message = err.Error()
			}
			ok = false
		}
		phases = append(phases, ph)
	}

	record("inject", func() (string, error) { return m.injectDepRestart(ctx, ns, target) })
	record("dependency_ready", func() (string, error) {
		return m.waitDepReady(ctx, ns, target, snapshot, spec.Timeout)
	})
	record("app_recovered", func() (string, error) {
		return m.waitAppHealthy(ctx, envName, ns, appPort, healthPath, spec.Timeout)
	})
	record("app_replicas_ready", func() (string, error) {
		return m.waitAppReplicas(ctx, ns, envName, spec.Timeout)
	})
	record("settle", func() (string, error) {
		return m.settle(ctx, envName, ns, target, appPort, healthPath, spec.Timeout)
	})

	detail.Phases = phases
	check := Check{
		Type:       CheckTypeDrillDepRestart,
		Status:     CheckPass,
		DurationMS: time.Since(start).Milliseconds(),
	}
	if b, err := json.Marshal(detail); err == nil {
		check.Detail = b
	}
	if ok {
		check.Findings = append(check.Findings, passFinding(phases, spec.Timeout))
	} else {
		check.Status = CheckFail
		check.Findings = append(check.Findings, failFinding(phases, target, spec.Timeout))
	}
	return check, nil
}

func passFinding(phases []DrillPhase, timeout time.Duration) Finding {
	depReady := phaseDuration(phases, "dependency_ready")
	appRec := phaseDuration(phases, "app_recovered")
	total := phaseDuration(phases, "inject") + depReady + appRec
	return Finding{
		Metric:   "recovery_seconds",
		Expected: fmt.Sprintf("<%s", timeout),
		Actual:   fmt.Sprintf("%.1f", total.Seconds()),
		Message: fmt.Sprintf("dependency restarted in %s; app recovered %s after injection",
			depReady.Round(100*time.Millisecond), appRec.Round(100*time.Millisecond)),
	}
}

func failFinding(phases []DrillPhase, target string, timeout time.Duration) Finding {
	for _, ph := range phases {
		if ph.Status == "pass" || ph.Status == "skipped" {
			continue
		}
		switch ph.Name {
		case "inject":
			return Finding{
				Metric:   "inject",
				Expected: "pod(s) deleted",
				Actual:   ph.Status,
				Message:  fmt.Sprintf("could not inject fault: %s", ph.Message),
			}
		case "dependency_ready":
			return Finding{
				Metric:   "dependency_ready_seconds",
				Expected: fmt.Sprintf("<%s", timeout),
				Actual:   fmt.Sprintf("timeout after %s", timeout),
				Message: fmt.Sprintf("%s did not become ready within %s — the dependency itself failed to recover",
					target, timeout),
			}
		case "app_recovered":
			depReady := phaseDuration(phases, "dependency_ready")
			return Finding{
				Metric:   "app_recovery_seconds",
				Expected: fmt.Sprintf("<%s", timeout),
				Actual:   fmt.Sprintf("timeout after %s", timeout),
				Message: fmt.Sprintf("%s restarted in %s, but the app never recovered within %s — likely missing reconnect/error-handling logic",
					target, depReady.Round(100*time.Millisecond), timeout),
			}
		case "app_replicas_ready":
			return Finding{
				Metric:   "app_replicas",
				Expected: "all replicas available",
				Actual:   ph.Message,
				Message:  fmt.Sprintf("app did not return to full capacity: %s", ph.Message),
			}
		case "settle":
			return Finding{
				Metric:   "steady_state",
				Expected: "app healthy after settling",
				Actual:   ph.Message,
				Message:  fmt.Sprintf("app unhealthy again after recovery: %s", ph.Message),
			}
		}
	}
	return Finding{Message: "drill failed"}
}

func phaseDuration(phases []DrillPhase, name string) time.Duration {
	for _, ph := range phases {
		if ph.Name == name {
			return time.Duration(ph.DurationMS) * time.Millisecond
		}
	}
	return 0
}

// fault injection

func (m *Manager) injectDepRestart(ctx context.Context, ns, target string) (string, error) {
	pods, err := m.Client.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{
		LabelSelector: "app=" + target,
	})
	if err != nil {
		return "", err
	}
	deleted := 0
	for i := range pods.Items {
		p := &pods.Items[i]
		if p.DeletionTimestamp != nil {
			continue
		}
		uid := p.UID
		err := m.Client.CoreV1().Pods(ns).Delete(ctx, p.Name, metav1.DeleteOptions{
			Preconditions: &metav1.Preconditions{UID: &uid},
		})
		if err != nil && !apierrors.IsNotFound(err) && !apierrors.IsConflict(err) {
			return "", fmt.Errorf("delete pod %s: %w", p.Name, err)
		}
		deleted++
	}
	if deleted == 0 {
		return "", fmt.Errorf("no running pods found for %q", target)
	}
	return fmt.Sprintf("deleted %d pod(s) of %s", deleted, target), nil
}

func (m *Manager) podUIDs(ctx context.Context, ns, target string) (map[string]bool, error) {
	pods, err := m.Client.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{
		LabelSelector: "app=" + target,
	})
	if err != nil {
		return nil, err
	}
	uids := map[string]bool{}
	for i := range pods.Items {
		uids[string(pods.Items[i].UID)] = true
	}
	return uids, nil
}

// recovery observation

func (m *Manager) waitDepReady(ctx context.Context, ns, target string, old map[string]bool, timeout time.Duration) (string, error) {
	pctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	t0 := time.Now()
	for {
		pods, err := m.Client.CoreV1().Pods(ns).List(pctx, metav1.ListOptions{
			LabelSelector: "app=" + target,
		})
		if err != nil {
			return "", err
		}
		for i := range pods.Items {
			p := &pods.Items[i]
			if old[string(p.UID)] || p.DeletionTimestamp != nil {
				continue
			}
			if podReady(p) {
				return fmt.Sprintf("%s ready again after %s", target,
					time.Since(t0).Round(100*time.Millisecond)), nil
			}
		}
		select {
		case <-pctx.Done():
			if ctx.Err() != nil {
				return "", ctx.Err()
			}
			return fmt.Sprintf("%s not ready within %s", target, timeout), context.DeadlineExceeded
		case <-time.After(drillPollInterval):
		}
	}
}

func (m *Manager) waitAppHealthy(ctx context.Context, envName, ns string, port int32, healthPath string, timeout time.Duration) (string, error) {
	pctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	t0 := time.Now()
	host := fmt.Sprintf("%s.%s.svc.cluster.local", envName, ns)
	for {
		if err := m.probeFn(pctx, envName, host, port, healthPath); err == nil {
			return fmt.Sprintf("app healthy again after %s",
				time.Since(t0).Round(100*time.Millisecond)), nil
		}
		select {
		case <-pctx.Done():
			if ctx.Err() != nil {
				return "", ctx.Err()
			}
			return fmt.Sprintf("app did not recover within %s", timeout), context.DeadlineExceeded
		case <-time.After(drillPollInterval):
		}
	}
}

func (m *Manager) waitAppReplicas(ctx context.Context, ns, appName string, timeout time.Duration) (string, error) {
	pctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	for {
		d, err := m.Client.AppsV1().Deployments(ns).Get(pctx, appName, metav1.GetOptions{})
		if err == nil {
			want := int32(1)
			if d.Spec.Replicas != nil {
				want = *d.Spec.Replicas
			}
			if d.Status.AvailableReplicas >= want {
				return fmt.Sprintf("%d/%d app replicas available", d.Status.AvailableReplicas, want), nil
			}
		} else if !apierrors.IsNotFound(err) {
			return "", err
		}
		select {
		case <-pctx.Done():
			if ctx.Err() != nil {
				return "", ctx.Err()
			}
			d, _ := m.Client.AppsV1().Deployments(ns).Get(context.WithoutCancel(pctx), appName, metav1.GetOptions{})
			have := int32(0)
			want := int32(1)
			if d != nil {
				have = d.Status.AvailableReplicas
				if d.Spec.Replicas != nil {
					want = *d.Spec.Replicas
				}
			}
			return fmt.Sprintf("%d/%d", have, want), context.DeadlineExceeded
		case <-time.After(drillPollInterval):
		}
	}
}

func (m *Manager) settle(ctx context.Context, envName, ns, target string, port int32, healthPath string, timeout time.Duration) (string, error) {
	settleFor := 5 * time.Second
	if half := timeout / 2; half < settleFor {
		settleFor = half
	}
	if settleFor < time.Second {
		settleFor = time.Second
	}
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case <-time.After(settleFor):
	}
	host := fmt.Sprintf("%s.%s.svc.cluster.local", envName, ns)
	if err := m.probeFn(ctx, envName, host, port, healthPath); err != nil {
		return fmt.Sprintf("app unhealthy after settling: %v", err), err
	}
	pods, err := m.Client.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{
		LabelSelector: "app=" + target,
	})
	if err != nil {
		return "", err
	}
	for i := range pods.Items {
		p := &pods.Items[i]
		if p.DeletionTimestamp == nil && podReady(p) {
			return fmt.Sprintf("steady for %s", settleFor), nil
		}
	}
	return fmt.Sprintf("%s not ready after settling", target), fmt.Errorf("%s not ready after settling", target)
}

// probes

func (m *Manager) appPort(ctx context.Context, ns, envName string) (int32, error) {
	svc, err := m.Client.CoreV1().Services(ns).Get(ctx, envName, metav1.GetOptions{})
	if err != nil {
		return 0, fmt.Errorf("app service %q not found: %w", envName, err)
	}
	if len(svc.Spec.Ports) == 0 {
		return 0, fmt.Errorf("app service %q has no ports", envName)
	}
	return svc.Spec.Ports[0].Port, nil
}

func (m *Manager) appHealthPath(ctx context.Context, ns string) string {
	n, err := m.Client.CoreV1().Namespaces().Get(ctx, ns, metav1.GetOptions{})
	if err != nil {
		return ""
	}
	dir, ok := n.Annotations[AnnotationSliceDir]
	if !ok || dir == "" {
		return ""
	}
	s, _, err := slice.Load(dir)
	if err != nil || s.App.HealthCheck == nil {
		return ""
	}
	return s.App.HealthCheck.Path
}

func (m *Manager) defaultProbe(ctx context.Context, envName, host string, port int32, healthPath string) error {
	var script string
	if healthPath != "" {
		script = fmt.Sprintf("wget -qO- -T 3 http://%s:%d%s >/dev/null 2>&1", host, port, healthPath)
	} else {
		script = fmt.Sprintf("nc -z -w 3 %s %d >/dev/null 2>&1", host, port)
	}
	res, err := m.ExecToolbox(ctx, envName, []string{"sh", "-c", script})
	if err != nil {
		return err
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("probe failed (exit %d)", res.ExitCode)
	}
	return nil
}

func podReady(p *corev1.Pod) bool {
	for _, c := range p.Status.Conditions {
		if c.Type == corev1.PodReady {
			return c.Status == corev1.ConditionTrue
		}
	}
	return false
}
