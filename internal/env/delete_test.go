package env

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/kubotdev/kubox/internal/slice"
)

func TestDeleteWaitsForTermination(t *testing.T) {
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
		Name:   slice.EnvNamespace("shop"),
		Labels: map[string]string{LabelEnv: "shop"},
	}}
	cs := fake.NewSimpleClientset(ns)
	m := NewManager(cs, nil, "")

	gets := 0
	cs.PrependReactor("get", "namespaces", func(k8stesting.Action) (bool, runtime.Object, error) {
		gets++
		if gets <= 2 {
			return true, ns.DeepCopy(), nil // still terminating
		}
		return false, nil, nil // fall through to the tracker (deleted → NotFound)
	})

	if err := m.Delete(context.Background(), "shop"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if gets < 3 {
		t.Fatalf("expected the wait loop to poll until NotFound, got %d gets", gets)
	}
}

func TestDeleteMissingEnvIsNoop(t *testing.T) {
	m := NewManager(fake.NewSimpleClientset(), nil, "")
	if err := m.Delete(context.Background(), "shop"); err != nil {
		t.Fatalf("Delete missing env should be nil, got %v", err)
	}
}
