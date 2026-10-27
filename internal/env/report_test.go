package env

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/kubotdev/kubox/internal/slice"
)

// fixtures

func testNamespace(envName string) *corev1.Namespace {
	return &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name:   slice.EnvNamespace(envName),
			Labels: map[string]string{LabelEnv: envName},
		},
	}
}

func appDeployment(envName string, replicas, available int32) *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      envName,
			Namespace: slice.EnvNamespace(envName),
			Labels:    map[string]string{LabelManagedBy: ManagedByValue, LabelEnv: envName},
		},
		Spec:   appsv1.DeploymentSpec{Replicas: &replicas},
		Status: appsv1.DeploymentStatus{AvailableReplicas: available},
	}
}

func componentDeployment(envName, component string, replicas, available int32) *appsv1.Deployment {
	d := appDeployment(envName, replicas, available)
	d.Name = component
	return d
}

func appService(envName string, port int32) *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      envName,
			Namespace: slice.EnvNamespace(envName),
			Labels:    map[string]string{LabelEnv: envName},
		},
		Spec: corev1.ServiceSpec{Ports: []corev1.ServicePort{{Port: port}}},
	}
}

func componentService(envName, component string, port int32) *corev1.Service {
	s := appService(envName, port)
	s.Name = component
	return s
}

func appEndpoints(envName string, ready int) *corev1.Endpoints {
	ep := &corev1.Endpoints{
		ObjectMeta: metav1.ObjectMeta{Name: envName, Namespace: slice.EnvNamespace(envName)},
	}
	if ready > 0 {
		sub := corev1.EndpointSubset{}
		for i := 0; i < ready; i++ {
			sub.Addresses = append(sub.Addresses, corev1.EndpointAddress{IP: fmt.Sprintf("10.0.0.%d", i+1)})
		}
		ep.Subsets = []corev1.EndpointSubset{sub}
	}
	return ep
}

func componentPod(envName, component, name, uid string) *corev1.Pod {
	labels := commonLabels(envName, component)
	labels["app"] = component
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: slice.EnvNamespace(envName),
			Labels:    labels,
			UID:       types.UID(uid),
		},
		Status: corev1.PodStatus{
			Phase:      corev1.PodRunning,
			Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}},
		},
	}
}

func crashloopPod(envName, component, name string) *corev1.Pod {
	p := componentPod(envName, component, name, "uid-"+name)
	p.Status.ContainerStatuses = []corev1.ContainerStatus{{
		Name: component,
		State: corev1.ContainerState{
			Waiting: &corev1.ContainerStateWaiting{
				Reason:  "CrashLoopBackOff",
				Message: "back-off 40s restarting failed container",
			},
		},
	}}
	return p
}

func healthyEnvObjects(envName string) []runtime.Object {
	return []runtime.Object{
		testNamespace(envName),
		appDeployment(envName, 1, 1),
		appService(envName, 8080),
		appEndpoints(envName, 1),
	}
}

// CheckEnv

func TestCheckEnvHealthy(t *testing.T) {
	cs := fake.NewSimpleClientset(healthyEnvObjects("shop")...)
	m := NewManager(cs, nil, "")

	h, err := m.CheckEnv(context.Background(), "shop")
	if err != nil {
		t.Fatalf("CheckEnv: %v", err)
	}
	if !h.Healthy {
		t.Errorf("healthy = false, want true (problems: %+v)", h.Problems)
	}
	if h.Namespace != slice.EnvNamespace("shop") {
		t.Errorf("namespace = %q, want %q", h.Namespace, slice.EnvNamespace("shop"))
	}
}

func TestCheckEnvMissingNamespace(t *testing.T) {
	cs := fake.NewSimpleClientset()
	m := NewManager(cs, nil, "")

	h, err := m.CheckEnv(context.Background(), "ghost")
	if err != nil {
		t.Fatalf("CheckEnv: %v", err)
	}
	if h.Healthy {
		t.Error("healthy = true, want false")
	}
	if len(h.Problems) != 1 || h.Problems[0].Kind != "NotFound" {
		t.Errorf("problems = %+v, want one NotFound", h.Problems)
	}
}

func TestCheckEnvCrashLoopingPod(t *testing.T) {
	objs := healthyEnvObjects("shop")
	objs = append(objs,
		crashloopPod("shop", "postgres", "postgres-abc"),
		componentDeployment("shop", "postgres", 1, 0),
	)
	cs := fake.NewSimpleClientset(objs...)
	m := NewManager(cs, nil, "")

	h, err := m.CheckEnv(context.Background(), "shop")
	if err != nil {
		t.Fatalf("CheckEnv: %v", err)
	}
	if h.Healthy {
		t.Fatal("healthy = true, want false")
	}
	if len(h.Problems) != 1 {
		t.Fatalf("problems = %+v, want exactly one (root cause, not double-report)", h.Problems)
	}
	p := h.Problems[0]
	if p.Kind != "CrashLoopBackOff" || p.Component != "postgres" {
		t.Errorf("problem = %+v, want CrashLoopBackOff on postgres", p)
	}
}

func TestCheckEnvQuotaBlockedPod(t *testing.T) {
	objs := healthyEnvObjects("shop")
	p := componentPod("shop", "worker", "worker-1", "uid-worker")
	p.Status.Phase = corev1.PodPending
	p.Status.Conditions = []corev1.PodCondition{{
		Type:    corev1.PodScheduled,
		Status:  corev1.ConditionFalse,
		Reason:  "Unschedulable",
		Message: `0/3 nodes are available: 3 exceeded quota "kubox-quota", requested: pods=1`,
	}}
	objs = append(objs, p)
	cs := fake.NewSimpleClientset(objs...)
	m := NewManager(cs, nil, "")

	h, err := m.CheckEnv(context.Background(), "shop")
	if err != nil {
		t.Fatalf("CheckEnv: %v", err)
	}
	if h.Healthy {
		t.Fatal("healthy = true, want false")
	}
	found := false
	for _, prob := range h.Problems {
		if prob.Kind == "FailedQuota" {
			found = true
		}
	}
	if !found {
		t.Errorf("problems = %+v, want a FailedQuota entry", h.Problems)
	}
}

func TestCheckEnvNotReadyDeployment(t *testing.T) {
	objs := healthyEnvObjects("shop")
	objs = append(objs, componentDeployment("shop", "inventory", 2, 1))
	cs := fake.NewSimpleClientset(objs...)
	m := NewManager(cs, nil, "")

	h, err := m.CheckEnv(context.Background(), "shop")
	if err != nil {
		t.Fatalf("CheckEnv: %v", err)
	}
	if h.Healthy {
		t.Fatal("healthy = true, want false")
	}
	if len(h.Problems) != 1 || h.Problems[0].Kind != "NotReady" || h.Problems[0].Component != "inventory" {
		t.Errorf("problems = %+v, want one NotReady on inventory", h.Problems)
	}
}

func TestCheckEnvNoAppEndpoints(t *testing.T) {
	objs := []runtime.Object{
		testNamespace("shop"),
		appDeployment("shop", 1, 1),
		appService("shop", 8080),
		appEndpoints("shop", 0),
	}
	cs := fake.NewSimpleClientset(objs...)
	m := NewManager(cs, nil, "")

	h, err := m.CheckEnv(context.Background(), "shop")
	if err != nil {
		t.Fatalf("CheckEnv: %v", err)
	}
	if h.Healthy {
		t.Fatal("healthy = true, want false")
	}
	found := false
	for _, prob := range h.Problems {
		if prob.Kind == "NoEndpoints" {
			found = true
		}
	}
	if !found {
		t.Errorf("problems = %+v, want a NoEndpoints entry", h.Problems)
	}
}

func TestCheckEnvIgnoresTransientPods(t *testing.T) {
	objs := healthyEnvObjects("shop")
	k6 := crashloopPod("shop", "k6", "kubox-k6-123")
	objs = append(objs, k6)
	cs := fake.NewSimpleClientset(objs...)
	m := NewManager(cs, nil, "")

	h, err := m.CheckEnv(context.Background(), "shop")
	if err != nil {
		t.Fatalf("CheckEnv: %v", err)
	}
	if !h.Healthy {
		t.Errorf("healthy = false, want true — k6 pod failures are not env health (problems: %+v)", h.Problems)
	}
}

// LoadCheck

func TestLoadCheckPass(t *testing.T) {
	r := &K6Result{
		Status:     "pass",
		ExitCode:   0,
		Thresholds: map[string]K6Check{"http_req_duration_p95_lt_200": {Value: 142.3, Pass: true}},
	}
	c := LoadCheck(r, 3*time.Second)
	if c.Status != CheckPass || c.Type != CheckTypeLoad {
		t.Errorf("check = %+v, want passing load check", c)
	}
	if len(c.Findings) != 0 {
		t.Errorf("findings = %+v, want none", c.Findings)
	}
	var decoded K6Result
	if err := json.Unmarshal(c.Detail, &decoded); err != nil {
		t.Fatalf("detail does not decode as K6Result: %v", err)
	}
	if decoded.Status != "pass" || decoded.Thresholds["http_req_duration_p95_lt_200"].Value != 142.3 {
		t.Errorf("detail = %+v, want verbatim K6Result", decoded)
	}
}

func TestLoadCheckFail(t *testing.T) {
	r := &K6Result{
		Status:   "fail",
		ExitCode: 1,
		Thresholds: map[string]K6Check{
			"http_req_duration_p95_lt_200": {Value: 347, Pass: false},
			"http_req_failed_rate_lt_0_01": {Value: 0.002, Pass: true},
		},
	}
	c := LoadCheck(r, 3*time.Second)
	if c.Status != CheckFail {
		t.Errorf("status = %s, want fail", c.Status)
	}
	if len(c.Findings) != 1 {
		t.Fatalf("findings = %+v, want exactly the failing threshold", c.Findings)
	}
	f := c.Findings[0]
	if f.Metric != "http_req_duration_p95_lt_200" || f.Actual != "347" {
		t.Errorf("finding = %+v, want metric + actual for the crossed threshold", f)
	}
}

func TestLoadCheckFailWithoutThresholds(t *testing.T) {
	r := &K6Result{Status: "fail", ExitCode: 1}
	c := LoadCheck(r, time.Second)
	if c.Status != CheckFail {
		t.Errorf("status = %s, want fail", c.Status)
	}
	if len(c.Findings) != 1 || c.Findings[0].Message == "" {
		t.Errorf("findings = %+v, want a script-error hint", c.Findings)
	}
}

// NewReport

func TestNewReportStatuses(t *testing.T) {
	healthy := EnvHealth{Healthy: true, Namespace: "kubox-shop"}
	unhealthy := EnvHealth{Healthy: false, Namespace: "kubox-shop",
		Problems: []EnvProblem{{Kind: "CrashLoopBackOff"}}}
	pass := Check{Type: CheckTypeLoad, Status: CheckPass}
	fail := Check{Type: CheckTypeLoad, Status: CheckFail}
	errored := Check{Type: CheckTypeLoad, Status: CheckError}

	cases := []struct {
		name   string
		health EnvHealth
		checks []Check
		want   string
	}{
		{"pass", healthy, []Check{pass}, ReportPass},
		{"fail", healthy, []Check{pass, fail}, ReportFail},
		{"check error", healthy, []Check{errored}, ReportError},
		{"unhealthy env beats passing check", unhealthy, []Check{pass}, ReportError},
		{"unhealthy env, no checks", unhealthy, nil, ReportError},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := NewReport(tc.health, tc.checks...)
			if r.Status != tc.want {
				t.Errorf("status = %q, want %q", r.Status, tc.want)
			}
		})
	}
}
