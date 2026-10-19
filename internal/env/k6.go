package env

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/kubotdev/kubox/internal/slice"
)

const K6Image = "grafana/k6:0.54.0"

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
	Thresholds map[string]json.RawMessage `json:"thresholds"`
	Values     map[string]float64         `json:"-"`
}

func (m *k6Metric) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	m.Values = map[string]float64{}
	for k, v := range raw {
		switch k {
		case "thresholds":
			if err := json.Unmarshal(v, &m.Thresholds); err != nil {
				return err
			}
		case "values":
			var nested map[string]float64
			if err := json.Unmarshal(v, &nested); err == nil {
				for nk, nv := range nested {
					m.Values[nk] = nv
				}
			}
		default:
			var f float64
			if err := json.Unmarshal(v, &f); err == nil {
				m.Values[k] = f
			}
		}
	}
	return nil
}

type k6SummaryExport struct {
	Metrics map[string]k6Metric `json:"metrics"`
}

func (m *Manager) RunLoadTest(ctx context.Context, envName, scriptPath string) (*K6Result, error) {
	ns := slice.EnvNamespace(envName)

	if _, err := m.Client.CoreV1().Namespaces().Get(ctx, ns, metav1.GetOptions{}); err != nil {
		return nil, fmt.Errorf("environment %q not found — run `kubox up` first", envName)
	}
	svc, err := m.Client.CoreV1().Services(ns).Get(ctx, envName, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("app service %q not found in environment %q: %w", envName, envName, err)
	}
	if len(svc.Spec.Ports) == 0 {
		return nil, fmt.Errorf("app service %q has no ports", envName)
	}

	cmName, mounts, err := m.buildScriptConfigMap(ctx, ns, envName, scriptPath)
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = m.Client.CoreV1().ConfigMaps(ns).Delete(context.WithoutCancel(ctx), cmName, metav1.DeleteOptions{})
	}()

	endpoint := fmt.Sprintf("http://%s.%s.svc.cluster.local:%d", envName, ns, svc.Spec.Ports[0].Port)

	entry := filepath.Base(scriptPath)
	command := []string{
		"sh", "-c",
		fmt.Sprintf("k6 run --summary-export=/tmp/results.json %s; EXIT=$?; "+
			"echo %s; cat /tmp/results.json 2>/dev/null; "+
			"echo; echo %s; exit $EXIT",
			shellQuote("/scripts/"+entry), k6JSONMarker, k6JSONEndMarker),
	}

	result, err := m.runK6Pod(ctx, envName, ns, command, mounts, endpoint)
	if err != nil {
		return nil, err
	}
	return parseK6Result(result)
}

const (
	k6ScriptMaxFiles = 200
	k6ScriptMaxBytes = 950 * 1024
	k6ScriptMaxDepth = 3
)

var k6ScriptExts = map[string]bool{
	".js": true, ".mjs": true, ".cjs": true, ".json": true,
	".csv": true, ".txt": true, ".html": true, ".htm": true,
}

func (m *Manager) buildScriptConfigMap(ctx context.Context, ns, envName, scriptPath string) (string, []Mount, error) {
	dir := filepath.Dir(scriptPath)
	entryClean := filepath.Clean(scriptPath)
	cmName := fmt.Sprintf("k6-script-%d", time.Now().UnixNano()%1e9)

	data := map[string]string{}
	var items []corev1.KeyToPath
	usedKeys := map[string]bool{}
	total := 0
	otherFiles := 0
	entrySeen := false

	walkErr := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		isEntry := filepath.Clean(path) == entryClean
		if strings.HasPrefix(d.Name(), ".") {
			if d.IsDir() {
				return fs.SkipDir
			}
			if !isEntry {
				return nil
			}
		}
		if d.IsDir() {
			if strings.Count(rel, string(filepath.Separator))+1 >= k6ScriptMaxDepth {
				return fs.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		if !isEntry {
			if !k6ScriptExts[strings.ToLower(filepath.Ext(path))] {
				return nil
			}
			if otherFiles >= k6ScriptMaxFiles {
				return nil
			}
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		total += len(b)
		if total > k6ScriptMaxBytes {
			return fmt.Errorf("test scripts in %s exceed %dKB limit (ConfigMap bound)", dir, k6ScriptMaxBytes/1024)
		}
		mountRel := filepath.ToSlash(rel)
		key := dedupeScriptKey(usedKeys, scriptCMKey(mountRel))
		data[key] = string(b)
		items = append(items, corev1.KeyToPath{Key: key, Path: mountRel})
		if isEntry {
			entrySeen = true
		} else {
			otherFiles++
		}
		return nil
	})
	if walkErr != nil {
		if errors.Is(walkErr, fs.ErrNotExist) {
			return "", nil, fmt.Errorf("k6 script not found: %s", scriptPath)
		}
		return "", nil, fmt.Errorf("collect test scripts from %s: %w", dir, walkErr)
	}
	if !entrySeen {
		return "", nil, fmt.Errorf("k6 script not found: %s", scriptPath)
	}

	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      cmName,
			Namespace: ns,
			Labels:    commonLabels(envName, "k6-script"),
		},
		Data: data,
	}
	if _, err := m.Client.CoreV1().ConfigMaps(ns).Create(ctx, cm, metav1.CreateOptions{}); err != nil {
		return "", nil, fmt.Errorf("create k6 script configmap: %w", err)
	}
	return cmName, []Mount{{
		Name:          "scripts",
		MountPath:     "/scripts",
		ConfigMapName: cmName,
		Items:         items,
	}}, nil
}

func scriptCMKey(rel string) string {
	var b strings.Builder
	for _, r := range rel {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return b.String()
}

func dedupeScriptKey(used map[string]bool, base string) string {
	if !used[base] {
		used[base] = true
		return base
	}
	for i := 2; ; i++ {
		k := fmt.Sprintf("%s-%d", base, i)
		if !used[k] {
			used[k] = true
			return k
		}
	}
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
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
						Items:                mt.Items,
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
				Env: []corev1.EnvVar{
					{Name: "BASE_URL", Value: baseURL},
					{Name: "K6_SUMMARY_TREND_STATS", Value: "avg,min,med,max,p(90),p(95),p(99)"},
				},
				Resources: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("100m"),
						corev1.ResourceMemory: resource.MustParse("64Mi"),
					},
				},
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
			return m.collectPodResult(ctx, ns, name, "k6", p)
		}

		if reason, msg := podStartupFailure(p); reason != "" {
			return nil, fmt.Errorf("k6 pod failed to start: %s: %s", reason, msg)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}

func podStartupFailure(p *corev1.Pod) (reason, msg string) {
	for _, cs := range p.Status.ContainerStatuses {
		if w := cs.State.Waiting; w != nil {
			switch w.Reason {
			case "ErrImagePull", "ImagePullBackOff", "InvalidImageName", "CreateContainerConfigError":
				return w.Reason, w.Message
			}
		}
	}
	return "", ""
}

const (
	k6JSONMarker    = "===KUBOX_JSON==="
	k6JSONEndMarker = "===KUBOX_JSON_END==="
)

func parseK6Result(res *ExecResult) (*K6Result, error) {
	out := &K6Result{
		Status:   "fail",
		ExitCode: res.ExitCode,
	}
	if res.ExitCode == 0 {
		out.Status = "pass"
	}

	if raw := extractK6JSON(res.Stdout); raw != "" {
		var export k6SummaryExport
		if err := json.Unmarshal([]byte(raw), &export); err == nil {
			out.Thresholds = extractThresholds(export.Metrics)
			out.Summary = extractSummary(export.Metrics)
		}
	}
	return out, nil
}

func extractK6JSON(stdout string) string {
	trimmed := strings.TrimSpace(stdout)
	if i := strings.Index(trimmed, k6JSONMarker); i >= 0 {
		rest := trimmed[i+len(k6JSONMarker):]
		if j := strings.Index(rest, k6JSONEndMarker); j >= 0 {
			rest = rest[:j]
		}
		if s := strings.TrimSpace(rest); s != "" {
			return s
		}
		return ""
	}
	if strings.HasPrefix(trimmed, "{") {
		return trimmed
	}
	if i := strings.Index(trimmed, "{"); i >= 0 {
		if j := strings.LastIndex(trimmed, "}"); j > i {
			return trimmed[i : j+1]
		}
	}
	return ""
}

func decodeThresh(raw json.RawMessage) (crossed, ok bool) {
	var b bool
	if err := json.Unmarshal(raw, &b); err == nil {
		return b, true
	}
	var obj struct {
		OK *bool `json:"ok"`
	}
	if err := json.Unmarshal(raw, &obj); err == nil && obj.OK != nil {
		return *obj.OK, true
	}
	return false, false
}

func extractThresholds(metrics map[string]k6Metric) map[string]K6Check {
	out := map[string]K6Check{}
	for name, m := range metrics {
		for cond, raw := range m.Thresholds {
			crossed, ok := decodeThresh(raw)
			if !ok {
				continue
			}
			key := fmt.Sprintf("%s_%s", name, sanitizeThresholdKey(cond))
			out[key] = K6Check{
				Value: thresholdValue(m.Values, cond),
				Pass:  !crossed,
			}
		}
	}
	return out
}

func thresholdValue(values map[string]float64, cond string) float64 {
	stat := cond
	if i := strings.IndexAny(cond, "<>"); i >= 0 {
		stat = strings.TrimSpace(cond[:i])
	}
	if v, ok := values[stat]; ok {
		return v
	}
	return values["value"]
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
