package env

import (
	"context"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/kubotdev/kubox/internal/slice"
)

func TestDeployAppScalingAndHealth(t *testing.T) {
	replicas := 3
	s := &slice.Slice{
		Name: "shop",
		App: slice.App{
			Image:       "shop:v1",
			Port:        8080,
			Replicas:    &replicas,
			Scaling:     &slice.Scaling{Min: 2, Max: 8, CPUPercent: 60},
			HealthCheck: &slice.HealthCheck{Path: "/healthz", InitialDelaySeconds: 5},
		},
	}
	ns := slice.EnvNamespace("shop")
	cs := fake.NewSimpleClientset()
	m := NewManager(cs, nil, "")
	if err := m.deployApp(context.Background(), s, ns); err != nil {
		t.Fatalf("deployApp: %v", err)
	}

	dep, err := cs.AppsV1().Deployments(ns).Get(context.Background(), "shop", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get deployment: %v", err)
	}
	if *dep.Spec.Replicas != 2 {
		t.Errorf("deployment replicas = %d, want 2 (scaling.min)", *dep.Spec.Replicas)
	}
	c := dep.Spec.Template.Spec.Containers[0]
	if c.ReadinessProbe == nil || c.ReadinessProbe.HTTPGet == nil || c.ReadinessProbe.HTTPGet.Path != "/healthz" {
		t.Errorf("readiness probe missing or wrong: %+v", c.ReadinessProbe)
	}
	if c.LivenessProbe == nil || c.LivenessProbe.HTTPGet == nil || c.LivenessProbe.HTTPGet.Path != "/healthz" {
		t.Errorf("liveness probe missing or wrong: %+v", c.LivenessProbe)
	}
	if c.LivenessProbe.InitialDelaySeconds != 15 {
		t.Errorf("liveness initialDelay = %d, want 15 (readiness + 10)", c.LivenessProbe.InitialDelaySeconds)
	}

	hpa, err := cs.AutoscalingV2().HorizontalPodAutoscalers(ns).Get(context.Background(), "shop", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get hpa: %v", err)
	}
	if *hpa.Spec.MinReplicas != 2 || hpa.Spec.MaxReplicas != 8 {
		t.Errorf("hpa min/max = %d/%d, want 2/8", *hpa.Spec.MinReplicas, hpa.Spec.MaxReplicas)
	}
	if hpa.Spec.ScaleTargetRef.Kind != "Deployment" || hpa.Spec.ScaleTargetRef.Name != "shop" {
		t.Errorf("hpa target = %s/%s", hpa.Spec.ScaleTargetRef.Kind, hpa.Spec.ScaleTargetRef.Name)
	}
	if len(hpa.Spec.Metrics) != 1 || hpa.Spec.Metrics[0].Resource == nil ||
		*hpa.Spec.Metrics[0].Resource.Target.AverageUtilization != 60 {
		t.Errorf("hpa cpu metric wrong: %+v", hpa.Spec.Metrics)
	}
}

func TestDeployAppScalingPreservesLiveReplicas(t *testing.T) {
	replicas := 1
	ns := slice.EnvNamespace("shop")
	live := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "shop", Namespace: ns},
		Spec:       appsv1.DeploymentSpec{Replicas: int32ptr(5)},
	}
	cs := fake.NewSimpleClientset(live)
	s := &slice.Slice{
		Name: "shop",
		App: slice.App{
			Image:    "shop:v1",
			Port:     8080,
			Replicas: &replicas,
			Scaling:  &slice.Scaling{Min: 1, Max: 10, CPUPercent: 70},
		},
	}
	m := NewManager(cs, nil, "")
	if err := m.deployApp(context.Background(), s, ns); err != nil {
		t.Fatalf("deployApp: %v", err)
	}
	dep, err := cs.AppsV1().Deployments(ns).Get(context.Background(), "shop", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get deployment: %v", err)
	}
	if *dep.Spec.Replicas != 5 {
		t.Errorf("replicas = %d, want 5 (live HPA-managed value preserved)", *dep.Spec.Replicas)
	}
}

func TestDeployAppRemovesHPAWhenScalingRemoved(t *testing.T) {
	replicas := 1
	ns := slice.EnvNamespace("shop")
	existing := &autoscalingv2.HorizontalPodAutoscaler{
		ObjectMeta: metav1.ObjectMeta{Name: "shop", Namespace: ns},
		Spec:       autoscalingv2.HorizontalPodAutoscalerSpec{MaxReplicas: 3},
	}
	cs := fake.NewSimpleClientset(existing)
	s := &slice.Slice{
		Name: "shop",
		App:  slice.App{Image: "shop:v1", Port: 8080, Replicas: &replicas},
	}
	m := NewManager(cs, nil, "")
	if err := m.deployApp(context.Background(), s, ns); err != nil {
		t.Fatalf("deployApp: %v", err)
	}
	_, err := cs.AutoscalingV2().HorizontalPodAutoscalers(ns).Get(context.Background(), "shop", metav1.GetOptions{})
	if !apierrors.IsNotFound(err) {
		t.Errorf("expected hpa to be deleted, got err=%v", err)
	}
}
