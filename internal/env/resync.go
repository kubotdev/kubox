package env

import (
	"context"
	"fmt"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	AnnotationRestartedAt = "kubox.dev/restarted-at"
	readyTimeout          = 180 * time.Second
)

func (m *Manager) refreshTTL(ctx context.Context, ns *corev1.Namespace, expires time.Time) error {
	updated := ns.DeepCopy()
	if updated.Annotations == nil {
		updated.Annotations = map[string]string{}
	}
	updated.Annotations[AnnotationExpiresAt] = expires.Format(time.RFC3339)
	if _, err := m.Client.CoreV1().Namespaces().Update(ctx, updated, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("refresh environment ttl: %w", err)
	}
	return nil
}

func (m *Manager) waitDeployments(ctx context.Context, ns string) error {
	deadline := time.Now().Add(readyTimeout)
	for {
		list, err := m.Client.AppsV1().Deployments(ns).List(ctx, metav1.ListOptions{
			LabelSelector: LabelManagedBy + "=" + ManagedByValue,
		})
		if err != nil {
			return fmt.Errorf("list deployments: %w", err)
		}
		if deploymentsReady(list.Items) {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out after %s waiting for deployments in %s to become ready", readyTimeout, ns)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("waiting for deployments: %w", ctx.Err())
		case <-time.After(2 * time.Second):
		}
	}
}

func deploymentsReady(items []appsv1.Deployment) bool {
	for i := range items {
		d := &items[i]
		want := int32(1)
		if d.Spec.Replicas != nil {
			want = *d.Spec.Replicas
		}
		if d.Status.ObservedGeneration < d.Generation ||
			d.Status.UpdatedReplicas < want ||
			d.Status.AvailableReplicas < want ||
			d.Status.Replicas > want {
			return false
		}
	}
	return true
}
