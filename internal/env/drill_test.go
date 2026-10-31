package env

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/kubotdev/kubox/internal/slice"
)

// deleteAndRecreateReactor simulates the Deployment controller: when the
// drill deletes the dependency's pod, a replacement pod appears Ready.
func deleteAndRecreateReactor(cs *fake.Clientset, envName, component, newName, newUID string) {
	replacement := componentPod(envName, component, newName, newUID)
	cs.PrependReactor("delete", "pods", func(action k8stesting.Action) (bool, runtime.Object, error) {
		da := action.(k8stesting.DeleteAction)
		gvr := corev1.SchemeGroupVersion.WithResource("pods")
		if err := cs.Tracker().Delete(gvr, da.GetNamespace(), da.GetName()); err != nil {
			return true, nil, err
		}
		if err := cs.Tracker().Create(gvr, replacement, da.GetNamespace()); err != nil {
			return true, nil, err
		}
		return true, nil, nil
	})
}

// deleteOnlyReactor deletes the pod but never recreates it — the dependency
// never comes back.
func deleteOnlyReactor(cs *fake.Clientset) {
	cs.PrependReactor("delete", "pods", func(action k8stesting.Action) (bool, runtime.Object, error) {
		da := action.(k8stesting.DeleteAction)
		gvr := corev1.SchemeGroupVersion.WithResource("pods")
		if err := cs.Tracker().Delete(gvr, da.GetNamespace(), da.GetName()); err != nil {
			return true, nil, err
		}
		return true, nil, nil
	})
}

func drillEnvObjects(envName string) []runtime.Object {
	return append(healthyEnvObjects(envName),
		componentService(envName, "postgres", 5432),
		componentPod(envName, "postgres", "postgres-1", "uid-old"),
	)
}

func decodeDrillDetail(t *testing.T, c Check) DrillDetail {
	t.Helper()
	var d DrillDetail
	if err := json.Unmarshal(c.Detail, &d); err != nil {
		t.Fatalf("decode drill detail: %v", err)
	}
	return d
}

func phaseByName(t *testing.T, phases []DrillPhase, name string) DrillPhase {
	t.Helper()
	for _, ph := range phases {
		if ph.Name == name {
			return ph
		}
	}
	t.Fatalf("phase %q not found in %+v", name, phases)
	return DrillPhase{}
}

// validation

func TestRunDrillUnknownType(t *testing.T) {
	m := NewManager(fake.NewSimpleClientset(), nil, "")
	_, err := m.RunDrill(context.Background(), "shop", DrillSpec{Type: "partition", Dependency: "postgres"})
	if err == nil || !strings.Contains(err.Error(), "unknown drill type") {
		t.Errorf("err = %v, want unknown drill type", err)
	}
}

func TestRunDrillRequiresTarget(t *testing.T) {
	m := NewManager(fake.NewSimpleClientset(healthyEnvObjects("shop")...), nil, "")
	_, err := m.RunDrill(context.Background(), "shop", DrillSpec{Type: DrillDependencyRestart})
	if err == nil || !strings.Contains(err.Error(), "--dependency") {
		t.Errorf("err = %v, want --dependency hint", err)
	}
}

func TestRunDrillRefusesAppTarget(t *testing.T) {
	m := NewManager(fake.NewSimpleClientset(healthyEnvObjects("shop")...), nil, "")
	_, err := m.RunDrill(context.Background(), "shop", DrillSpec{Type: DrillDependencyRestart, Dependency: "shop"})
	if err == nil || !strings.Contains(err.Error(), "app itself") {
		t.Errorf("err = %v, want refusal to target the app itself", err)
	}
}

func TestRunDrillUnknownDependency(t *testing.T) {
	m := NewManager(fake.NewSimpleClientset(healthyEnvObjects("shop")...), nil, "")
	_, err := m.RunDrill(context.Background(), "shop", DrillSpec{Type: DrillDependencyRestart, Dependency: "redis"})
	if err == nil || !strings.Contains(err.Error(), "not a service") {
		t.Errorf("err = %v, want 'not a service'", err)
	}
}

func TestRunDrillUnhealthyEnvSkipsChecks(t *testing.T) {
	objs := append(healthyEnvObjects("shop"), crashloopPod("shop", "postgres", "postgres-1"))
	cs := fake.NewSimpleClientset(objs...)
	m := NewManager(cs, nil, "")

	res, err := m.RunDrill(context.Background(), "shop", DrillSpec{
		Type: DrillDependencyRestart, Dependency: "postgres", Timeout: time.Second,
	})
	if err != nil {
		t.Fatalf("RunDrill: %v", err)
	}
	if res.Check.Type != "" {
		t.Errorf("check = %+v, want zero-value (checks not run on unhealthy env)", res.Check)
	}
	if res.Environment.Healthy {
		t.Error("environment reported healthy despite crashlooping pod")
	}
}

// dependency-restart

func TestRunDrillHappyPath(t *testing.T) {
	cs := fake.NewSimpleClientset(drillEnvObjects("shop")...)
	deleteAndRecreateReactor(cs, "shop", "postgres", "postgres-2", "uid-new")
	m := NewManager(cs, nil, "")
	m.probeFn = func(context.Context, string, string, int32, string) error { return nil }

	res, err := m.RunDrill(context.Background(), "shop", DrillSpec{
		Type: DrillDependencyRestart, Dependency: "postgres", Timeout: 2 * time.Second,
	})
	if err != nil {
		t.Fatalf("RunDrill: %v", err)
	}
	if !res.Environment.Healthy {
		t.Error("environment reported unhealthy at preflight")
	}
	if res.Check.Status != CheckPass {
		t.Errorf("status = %s, want pass (findings: %+v)", res.Check.Status, res.Check.Findings)
	}
	d := decodeDrillDetail(t, res.Check)
	if d.Target != "postgres" || d.Fault != "delete-pods" {
		t.Errorf("detail = %+v, want target postgres / delete-pods", d)
	}
	for _, name := range []string{"inject", "dependency_ready", "app_recovered", "app_replicas_ready", "settle"} {
		if ph := phaseByName(t, d.Phases, name); ph.Status != "pass" {
			t.Errorf("phase %s = %s (%s), want pass", name, ph.Status, ph.Message)
		}
	}
	if len(res.Check.Findings) != 1 || res.Check.Findings[0].Metric != "recovery_seconds" {
		t.Errorf("findings = %+v, want one recovery_seconds finding", res.Check.Findings)
	}
}

func TestRunDrillDependencyNeverReady(t *testing.T) {
	cs := fake.NewSimpleClientset(drillEnvObjects("shop")...)
	deleteOnlyReactor(cs)
	m := NewManager(cs, nil, "")
	m.probeFn = func(context.Context, string, string, int32, string) error { return nil }

	res, err := m.RunDrill(context.Background(), "shop", DrillSpec{
		Type: DrillDependencyRestart, Dependency: "postgres", Timeout: time.Second,
	})
	if err != nil {
		t.Fatalf("RunDrill: %v", err)
	}
	if res.Check.Status != CheckFail {
		t.Errorf("status = %s, want fail", res.Check.Status)
	}
	d := decodeDrillDetail(t, res.Check)
	if ph := phaseByName(t, d.Phases, "dependency_ready"); ph.Status != "timeout" {
		t.Errorf("dependency_ready = %s, want timeout", ph.Status)
	}
	for _, name := range []string{"app_recovered", "app_replicas_ready", "settle"} {
		if ph := phaseByName(t, d.Phases, name); ph.Status != "skipped" {
			t.Errorf("phase %s = %s, want skipped after a timeout", name, ph.Status)
		}
	}
	if len(res.Check.Findings) != 1 || res.Check.Findings[0].Metric != "dependency_ready_seconds" {
		t.Errorf("findings = %+v, want dependency_ready_seconds diagnosis", res.Check.Findings)
	}
	if !strings.Contains(res.Check.Findings[0].Message, "failed to recover") {
		t.Errorf("finding message = %q, want dependency-side diagnosis", res.Check.Findings[0].Message)
	}
}

func TestRunDrillAppNeverRecovers(t *testing.T) {
	cs := fake.NewSimpleClientset(drillEnvObjects("shop")...)
	deleteAndRecreateReactor(cs, "shop", "postgres", "postgres-2", "uid-new")
	m := NewManager(cs, nil, "")
	// The dependency comes back, but the app never answers its probe again.
	m.probeFn = func(context.Context, string, string, int32, string) error {
		return context.DeadlineExceeded
	}

	res, err := m.RunDrill(context.Background(), "shop", DrillSpec{
		Type: DrillDependencyRestart, Dependency: "postgres", Timeout: time.Second,
	})
	if err != nil {
		t.Fatalf("RunDrill: %v", err)
	}
	if res.Check.Status != CheckFail {
		t.Errorf("status = %s, want fail", res.Check.Status)
	}
	d := decodeDrillDetail(t, res.Check)
	if ph := phaseByName(t, d.Phases, "dependency_ready"); ph.Status != "pass" {
		t.Errorf("dependency_ready = %s, want pass — the dependency itself recovered", ph.Status)
	}
	if ph := phaseByName(t, d.Phases, "app_recovered"); ph.Status != "timeout" {
		t.Errorf("app_recovered = %s, want timeout", ph.Status)
	}
	f := res.Check.Findings[0]
	if f.Metric != "app_recovery_seconds" || !strings.Contains(f.Message, "never recovered") {
		t.Errorf("finding = %+v, want app-side reconnect diagnosis", f)
	}
}

// scale-under-load

func scaleEnvObjects(envName string) []runtime.Object {
	return append(healthyEnvObjects(envName),
		componentPod(envName, envName, envName+"-1", "uid-app"),
	)
}

func scaleHPA(envName string, max int32, active bool) *autoscalingv2.HorizontalPodAutoscaler {
	min := int32(1)
	hpa := &autoscalingv2.HorizontalPodAutoscaler{
		ObjectMeta: metav1.ObjectMeta{
			Name:      envName,
			Namespace: slice.EnvNamespace(envName),
			Labels:    map[string]string{LabelEnv: envName},
		},
		Spec: autoscalingv2.HorizontalPodAutoscalerSpec{
			ScaleTargetRef: autoscalingv2.CrossVersionObjectReference{
				APIVersion: "apps/v1", Kind: "Deployment", Name: envName,
			},
			MinReplicas: &min,
			MaxReplicas: max,
		},
	}
	cond := autoscalingv2.HorizontalPodAutoscalerCondition{Type: autoscalingv2.ScalingActive}
	if active {
		cond.Status = corev1.ConditionTrue
		cond.Reason = "ValidMetricsFound"
		cond.Message = "the HPA was able to successfully calculate a replica count from cpu utilization"
	} else {
		cond.Status = corev1.ConditionFalse
		cond.Reason = "FailedGetResourceMetric"
		cond.Message = "the HPA was unable to compute the replica count: failed to get cpu utilization"
	}
	hpa.Status.Conditions = []autoscalingv2.HorizontalPodAutoscalerCondition{cond}
	return hpa
}

// scaleDeployment sets the app deployment's replicas, standing in for the
// HPA controller reacting to load.
func scaleDeployment(t *testing.T, cs *fake.Clientset, envName string, replicas int32) {
	t.Helper()
	ns := slice.EnvNamespace(envName)
	d, err := cs.AppsV1().Deployments(ns).Get(context.Background(), envName, metav1.GetOptions{})
	if err != nil {
		t.Errorf("get deployment: %v", err)
		return
	}
	d.Spec.Replicas = &replicas
	if _, err := cs.AppsV1().Deployments(ns).Update(context.Background(), d, metav1.UpdateOptions{}); err != nil {
		t.Errorf("update deployment: %v", err)
	}
}

func TestRunDrillScaleUnderLoadRequiresScript(t *testing.T) {
	objs := append(scaleEnvObjects("shop"), scaleHPA("shop", 3, true))
	m := NewManager(fake.NewSimpleClientset(objs...), nil, "")
	_, err := m.RunDrill(context.Background(), "shop", DrillSpec{Type: DrillScaleLoad})
	if err == nil || !strings.Contains(err.Error(), "--script") {
		t.Errorf("err = %v, want --script hint", err)
	}
}

func TestRunDrillScaleUnderLoadRequiresHPA(t *testing.T) {
	m := NewManager(fake.NewSimpleClientset(healthyEnvObjects("shop")...), nil, "")
	_, err := m.RunDrill(context.Background(), "shop", DrillSpec{Type: DrillScaleLoad, Script: "load.js"})
	if err == nil || !strings.Contains(err.Error(), "app.scaling") {
		t.Errorf("err = %v, want app.scaling hint", err)
	}
}

func TestRunDrillScaleUnderLoadNoHeadroom(t *testing.T) {
	objs := append(scaleEnvObjects("shop"), scaleHPA("shop", 1, true))
	m := NewManager(fake.NewSimpleClientset(objs...), nil, "")
	_, err := m.RunDrill(context.Background(), "shop", DrillSpec{Type: DrillScaleLoad, Script: "load.js"})
	if err == nil || !strings.Contains(err.Error(), "scaling.max") {
		t.Errorf("err = %v, want scaling.max headroom hint", err)
	}
}

func TestRunDrillScaleUnderLoad(t *testing.T) {
	objs := append(scaleEnvObjects("shop"), scaleHPA("shop", 3, true))
	cs := fake.NewSimpleClientset(objs...)
	m := NewManager(cs, nil, "")
	m.probeFn = func(context.Context, string, string, int32, string) error { return nil }
	m.loadFn = func(ctx context.Context, envName, script string) (*K6Result, error) {
		// The HPA reacts while the load runs.
		scaleDeployment(t, cs, envName, 2)
		return &K6Result{Status: "pass", Summary: K6Summary{Requests: 1500, RPS: 90, P95: 4.9}}, nil
	}

	res, err := m.RunDrill(context.Background(), "shop", DrillSpec{
		Type: DrillScaleLoad, Script: "load.js", Timeout: 3 * time.Second,
	})
	if err != nil {
		t.Fatalf("RunDrill: %v", err)
	}
	if res.Check.Status != CheckPass {
		t.Errorf("status = %s, want pass (findings: %+v)", res.Check.Status, res.Check.Findings)
	}
	if res.Check.Type != CheckTypeDrillScaleLoad {
		t.Errorf("type = %q, want %q", res.Check.Type, CheckTypeDrillScaleLoad)
	}
	d := decodeDrillDetail(t, res.Check)
	if d.Fault != "hpa-scale-under-load" || d.Target != "shop" {
		t.Errorf("detail = %+v, want target shop / hpa-scale-under-load", d)
	}
	for _, name := range []string{"baseline", "scale_up", "load_slo", "settle"} {
		if ph := phaseByName(t, d.Phases, name); ph.Status != "pass" {
			t.Errorf("phase %s = %s (%s), want pass", name, ph.Status, ph.Message)
		}
	}
	if len(res.Check.Findings) != 1 || res.Check.Findings[0].Metric != "scale_up_seconds" {
		t.Errorf("findings = %+v, want one scale_up_seconds finding", res.Check.Findings)
	}
}

func TestRunDrillScaleUnderLoadNoMetrics(t *testing.T) {
	objs := append(scaleEnvObjects("shop"), scaleHPA("shop", 3, false))
	m := NewManager(fake.NewSimpleClientset(objs...), nil, "")
	m.probeFn = func(context.Context, string, string, int32, string) error { return nil }
	loaded := false
	m.loadFn = func(context.Context, string, string) (*K6Result, error) {
		loaded = true
		return &K6Result{Status: "pass"}, nil
	}

	res, err := m.RunDrill(context.Background(), "shop", DrillSpec{
		Type: DrillScaleLoad, Script: "load.js", Timeout: time.Second,
	})
	if err != nil {
		t.Fatalf("RunDrill: %v", err)
	}
	if res.Check.Status != CheckFail {
		t.Errorf("status = %s, want fail", res.Check.Status)
	}
	d := decodeDrillDetail(t, res.Check)
	if ph := phaseByName(t, d.Phases, "baseline"); ph.Status != "timeout" {
		t.Errorf("baseline = %s, want timeout", ph.Status)
	}
	for _, name := range []string{"scale_up", "load_slo", "settle"} {
		if ph := phaseByName(t, d.Phases, name); ph.Status != "skipped" {
			t.Errorf("phase %s = %s, want skipped after a failed baseline", name, ph.Status)
		}
	}
	if loaded {
		t.Error("load ran despite a failed baseline")
	}
	f := res.Check.Findings[0]
	if f.Metric != "scaling_active" || !strings.Contains(f.Message, "metrics-server") {
		t.Errorf("finding = %+v, want metrics-server hint", f)
	}
}

func TestRunDrillScaleUnderLoadSLOFails(t *testing.T) {
	objs := append(scaleEnvObjects("shop"), scaleHPA("shop", 3, true))
	cs := fake.NewSimpleClientset(objs...)
	m := NewManager(cs, nil, "")
	m.probeFn = func(context.Context, string, string, int32, string) error { return nil }
	m.loadFn = func(ctx context.Context, envName, script string) (*K6Result, error) {
		// Scales up fine — but the agent's SLO misses.
		scaleDeployment(t, cs, envName, 2)
		return &K6Result{
			Status:     "fail",
			Thresholds: map[string]K6Check{"http_req_duration_p95_lt_200": {Value: 347, Pass: false}},
		}, nil
	}

	res, err := m.RunDrill(context.Background(), "shop", DrillSpec{
		Type: DrillScaleLoad, Script: "load.js", Timeout: 3 * time.Second,
	})
	if err != nil {
		t.Fatalf("RunDrill: %v", err)
	}
	if res.Check.Status != CheckFail {
		t.Errorf("status = %s, want fail", res.Check.Status)
	}
	d := decodeDrillDetail(t, res.Check)
	if ph := phaseByName(t, d.Phases, "scale_up"); ph.Status != "pass" {
		t.Errorf("scale_up = %s (%s), want pass — only the SLO failed", ph.Status, ph.Message)
	}
	if ph := phaseByName(t, d.Phases, "load_slo"); ph.Status != "failed" {
		t.Errorf("load_slo = %s, want failed", ph.Status)
	}
	f := res.Check.Findings[0]
	if f.Metric != "slo" || !strings.Contains(f.Message, "threshold not met") {
		t.Errorf("finding = %+v, want SLO threshold diagnosis", f)
	}
}

func TestRunDrillScaleUnderLoadNoScaleUp(t *testing.T) {
	objs := append(scaleEnvObjects("shop"), scaleHPA("shop", 3, true))
	m := NewManager(fake.NewSimpleClientset(objs...), nil, "")
	m.probeFn = func(context.Context, string, string, int32, string) error { return nil }
	m.loadFn = func(context.Context, string, string) (*K6Result, error) {
		return &K6Result{Status: "pass", Summary: K6Summary{Requests: 100}}, nil
	}

	res, err := m.RunDrill(context.Background(), "shop", DrillSpec{
		Type: DrillScaleLoad, Script: "load.js", Timeout: time.Second,
	})
	if err != nil {
		t.Fatalf("RunDrill: %v", err)
	}
	if res.Check.Status != CheckFail {
		t.Errorf("status = %s, want fail", res.Check.Status)
	}
	d := decodeDrillDetail(t, res.Check)
	if ph := phaseByName(t, d.Phases, "scale_up"); ph.Status != "timeout" {
		t.Errorf("scale_up = %s (%s), want timeout", ph.Status, ph.Message)
	}
	if ph := phaseByName(t, d.Phases, "load_slo"); ph.Status != "skipped" {
		t.Errorf("load_slo = %s, want skipped", ph.Status)
	}
	f := res.Check.Findings[0]
	if f.Metric != "replicas" || !strings.Contains(f.Message, "replicas") {
		t.Errorf("finding = %+v, want scale-up diagnosis", f)
	}
}
