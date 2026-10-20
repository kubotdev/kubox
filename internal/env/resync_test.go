package env

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/kubotdev/kubox/internal/slice"
)

const resyncSliceYAML = `
name: shop
app:
  image: demo:v1
  port: 8080
  env:
    DEMO_ROLE: orders
dependencies:
  - name: inventory
    image: demo:v1
    port: 8080
    volume:
      size: 100Mi
      path: /data
quotas:
  cpu: "1"
  memory: 768Mi
  pods: "10"
`

func loadTestSlice(t *testing.T) *slice.Slice {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "kubox.yaml"), []byte(resyncSliceYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	s, _, err := slice.Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return s
}

func TestEnsureCreateThenResync(t *testing.T) {
	s := loadTestSlice(t)
	cs := fake.NewSimpleClientset()
	m := NewManager(cs, nil, "")
	m.waitFn = func(context.Context, string) error { return nil }
	ctx := context.Background()

	first, err := m.ensure(ctx, s, time.Hour)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if first.Status != "ready" {
		t.Errorf("first status = %q, want ready", first.Status)
	}
	deps, err := cs.AppsV1().Deployments(first.Namespace).List(ctx, metav1.ListOptions{})
	if err != nil || len(deps.Items) != 2 {
		t.Fatalf("deployments after create = %d (err %v), want 2", len(deps.Items), err)
	}
	if _, err := cs.CoreV1().Services(first.Namespace).Get(ctx, "shop", metav1.GetOptions{}); err != nil {
		t.Fatalf("service shop missing: %v", err)
	}
	if _, err := cs.CoreV1().PersistentVolumeClaims(first.Namespace).Get(ctx, "inventory-data", metav1.GetOptions{}); err != nil {
		t.Fatalf("pvc inventory-data missing: %v", err)
	}
	inv, err := cs.AppsV1().Deployments(first.Namespace).Get(ctx, "inventory", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if inv.Spec.Strategy.Type != appsv1.RecreateDeploymentStrategyType {
		t.Errorf("inventory strategy = %q, want Recreate", inv.Spec.Strategy.Type)
	}
	if inv.Spec.Template.Spec.Containers[0].VolumeMounts[0].MountPath != "/data" {
		t.Errorf("inventory volume mount = %q, want /data", inv.Spec.Template.Spec.Containers[0].VolumeMounts[0].MountPath)
	}

	dep, err := cs.AppsV1().Deployments(first.Namespace).Get(ctx, "shop", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	dep.Spec.Template.Spec.Containers[0].Image = "stale:old"
	dep.Spec.Template.Annotations = nil
	if _, err := cs.AppsV1().Deployments(first.Namespace).Update(ctx, dep, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}

	ns, err := cs.CoreV1().Namespaces().Get(ctx, first.Namespace, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	ns.Annotations[AnnotationExpiresAt] = "2020-01-01T00:00:00Z"
	if _, err := cs.CoreV1().Namespaces().Update(ctx, ns, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}

	leftover := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "removed-dep",
			Namespace: first.Namespace,
			Labels:    map[string]string{LabelEnv: s.Name, LabelManagedBy: ManagedByValue},
		},
	}
	if _, err := cs.AppsV1().Deployments(first.Namespace).Create(ctx, leftover, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}

	second, err := m.ensure(ctx, s, time.Hour)
	if err != nil {
		t.Fatalf("resync: %v", err)
	}
	if second.Status != "resynced" {
		t.Errorf("second status = %q, want resynced", second.Status)
	}
	if second.Namespace != first.Namespace {
		t.Errorf("resync namespace = %q, want %q", second.Namespace, first.Namespace)
	}

	rolled, err := cs.AppsV1().Deployments(first.Namespace).Get(ctx, "shop", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if img := rolled.Spec.Template.Spec.Containers[0].Image; img != "demo:v1" {
		t.Errorf("image after resync = %q, want demo:v1", img)
	}
	if rolled.Spec.Template.Annotations[AnnotationRestartedAt] == "" {
		t.Error("restart annotation missing after resync")
	}

	ns, err = cs.CoreV1().Namespaces().Get(ctx, first.Namespace, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	expires, err := time.Parse(time.RFC3339, ns.Annotations[AnnotationExpiresAt])
	if err != nil {
		t.Fatalf("expires annotation: %v", err)
	}
	if expires.Year() < 2021 {
		t.Errorf("ttl not refreshed: %s", expires)
	}

	if _, err := cs.AppsV1().Deployments(first.Namespace).Get(ctx, "removed-dep", metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Errorf("pruned deployment still exists (err %v)", err)
	}
}

func TestEnsureRejectsForeignNamespace(t *testing.T) {
	s := loadTestSlice(t)
	ns := slice.EnvNamespace(s.Name)
	cs := fake.NewSimpleClientset(&corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: ns},
	})
	m := NewManager(cs, nil, "")

	_, err := m.ensure(context.Background(), s, time.Hour)
	if err == nil {
		t.Fatal("expected error for namespace not managed by kubox")
	}
	if want := "not a kubox environment"; !strings.Contains(err.Error(), want) {
		t.Errorf("error = %q, want substring %q", err, want)
	}
}
