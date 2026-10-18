package env

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/kubotdev/kubox/internal/slice"
)

const K6Image = "grafana/k6:0.54"

type K6Result struct {
	Status     string             `json:"status"` // "pass" or "fail"
	ExitCode   int                `json:"exit_code"`
	Thresholds map[string]K6Check `json:"thresholds"`
	Summary    K6Summary          `json:"summary"`
}

type K6Check struct {
	Value float64 `json:"value"`
	Pass  bool    `json:"pass"`
}

type K6Summary struct {
	Requests int     `json:"requests"`
	RPS      float64 `json:"rps"`
	P50      float64 `json:"p50"`
	P95      float64 `json:"p95"`
	P99      float64 `json:"p99"`
}

type k6Metric struct {
	Values     map[string]float64  `json:"values"`
	Thresholds map[string]k6Thresh `json:"thresholds"`
}

type k6Thresh struct {
	OK bool `json:"ok"`
}

type k6SummaryExport struct {
	Metrics map[string]k6Metric `json:"metrics"`
}

func (m *Manager) RunLoadTest(ctx context.Context, envName, scriptPath string) (*K6Result, error) {
	ns := slice.EnvNamespace(envName)

	if _, err := m.Client.CoreV1().Namespaces().Get(ctx, ns, metav1.GetOptions{}); err != nil {
		return nil, fmt.Errorf("environment %q not found — run `kubox up` first", envName)
	}

	script, err := os.ReadFile(scriptPath)
	if err != nil {
		return nil, fmt.Errorf("read k6 script: %w", err)
	}

	cmName := fmt.Sprintf("k6-script-%d", time.Now().UnixNano()%1e9)
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      cmName,
			Namespace: ns,
			Labels:    commonLabels(envName, "k6-script"),
		},
		Data: map[string]string{"load.js": string(script)},
	}
	if _, err := m.Client.CoreV1().ConfigMaps(ns).Create(ctx, cm, metav1.CreateOptions{}); err != nil {
		return nil, fmt.Errorf("create k6 script configmap: %w", err)
	}
	defer func() {
		_ = m.Client.CoreV1().ConfigMaps(ns).Delete(context.WithoutCancel(ctx), cmName, metav1.DeleteOptions{})
	}()

	endpoint := fmt.Sprintf("http://%s.%s.svc.cluster.local:%d", envName, ns, appPortForEnv(envName))

	command := []string{
		"sh", "-c",
		"k6 run --summary-export=/tmp/results.json /scripts/load.js; EXIT=$?; cat /tmp/results.json 2>/dev/null; exit $EXIT",
	}

	mounts := []Mount{{
		Name:          "scripts",
		MountPath:     "/scripts",
		ConfigMapName: cmName,
	}}

	result, err := m.runK6Pod(ctx, envName, ns, command, mounts, endpoint)
	if err != nil {
		return nil, err
	}
	return parseK6Result(result)
}

func (m *Manager) runK6Pod(ctx context.Context, envName, ns string, command []string, mounts []Mount, baseURL string) (*ExecResult, error) {
	name := fmt.Sprintf("kubox-k6-%d", time.Now().UnixNano()%1e9)

	var volMounts []corev1.VolumeMount
	var volumes []corev1.Volume
	for _, mt := range mounts {
		volMounts = append(volMounts, corev1.VolumeMount{Name: mt.Name, MountPath: mt.MountPath})
		if mt.ConfigMapName != "" {
			volumes = append(volumes, corev1.Volume{
				Name: mt.Name,
				VolumeSource: corev1.VolumeSource{
					ConfigMap: &corev1.ConfigMapVolumeSource{
						LocalObjectReference: corev1.LocalObjectReference{Name: mt.ConfigMapName},
					},
				},
			})
		}
	}

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: ns,
			Labels:    commonLabels(envName, "k6"),
		},
		Spec: corev1.PodSpec{
			RestartPolicy:      corev1.RestartPolicyNever,
			ServiceAccountName: "kubox-toolbox",
			Containers: []corev1.Container{{
				Name:         "k6",
				Image:        K6Image,
				Command:      command,
				VolumeMounts: volMounts,
				Env: []corev1.EnvVar{{
					Name:  "BASE_URL",
					Value: baseURL,
				}},
			}},
			Volumes: volumes,
		},
	}
	if _, err := m.Client.CoreV1().Pods(ns).Create(ctx, pod, metav1.CreateOptions{}); err != nil {
		return nil, fmt.Errorf("create k6 pod: %w", err)
	}
	defer func() {
		_ = m.Client.CoreV1().Pods(ns).Delete(context.WithoutCancel(ctx), name, metav1.DeleteOptions{})
	}()

	// Wait for terminal phase.
	deadline := time.Now().Add(10 * time.Minute)
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("k6 pod timed out")
		}
		p, err := m.Client.CoreV1().Pods(ns).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return nil, err
		}
		if p.Status.Phase == corev1.PodSucceeded || p.Status.Phase == corev1.PodFailed {
			return m.collectPodResult(ctx, ns, name, p)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}

func appPortForEnv(_ string) int { return 8080 }

func parseK6Result(res *ExecResult) (*K6Result, error) {
	out := &K6Result{
		Status:   "fail",
		ExitCode: res.ExitCode,
	}
	if res.ExitCode == 0 {
		out.Status = "pass"
	}

	jsonStart := strings.Index(res.Stdout, "{")
	jsonEnd := strings.LastIndex(res.Stdout, "}")
	if jsonStart >= 0 && jsonEnd > jsonStart {
		var raw k6SummaryExport
		if err := json.Unmarshal([]byte(res.Stdout[jsonStart:jsonEnd+1]), &raw); err == nil {
			out.Thresholds = extractThresholds(raw.Metrics)
			out.Summary = extractSummary(raw.Metrics)
		}
	}
	return out, nil
}

func extractThresholds(metrics map[string]k6Metric) map[string]K6Check {
	out := map[string]K6Check{}
	for name, m := range metrics {
		for cond, t := range m.Thresholds {
			key := fmt.Sprintf("%s_%s", name, sanitizeThresholdKey(cond))
			out[key] = K6Check{
				Value: m.Values["value"],
				Pass:  t.OK,
			}
		}
	}
	return out
}

func sanitizeThresholdKey(s string) string {
	s = strings.ReplaceAll(s, "(", "")
	s = strings.ReplaceAll(s, ")", "")
	s = strings.ReplaceAll(s, "<", "_lt_")
	s = strings.ReplaceAll(s, ">", "_gt_")
	return s
}

func extractSummary(metrics map[string]k6Metric) K6Summary {
	s := K6Summary{}
	if m, ok := metrics["http_reqs"]; ok {
		s.Requests = int(m.Values["count"])
		s.RPS = m.Values["rate"]
	}
	if m, ok := metrics["http_req_duration"]; ok {
		s.P50 = m.Values["med"]
		s.P95 = m.Values["p(95)"]
		s.P99 = m.Values["p(99)"]
	}
	return s
}

func ResolveScriptPath(sliceDir, arg string) string {
	if _, err := os.Stat(arg); err == nil {
		return arg
	}
	for _, candidate := range []string{
		filepath.Join(sliceDir, "tests", arg),
		filepath.Join(sliceDir, "tests", arg+".js"),
	} {
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	return arg
}
