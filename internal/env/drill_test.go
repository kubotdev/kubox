package env

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
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
