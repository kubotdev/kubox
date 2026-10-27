package env

import (
	"context"
	"fmt"
	"sort"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/kubotdev/kubox/internal/slice"
)

const (
	// LabelEnv marks namespaces created by kubox and carries the env name.
	LabelEnv = "kubox.dev/env"
	// LabelComponent marks which part of the slice a resource belongs to.
	LabelComponent = "kubox.dev/component"
	// LabelManagedBy is the standard app.kubernetes.io/managed-by value.
	LabelManagedBy = "app.kubernetes.io/managed-by"
	// ManagedByValue identifies kubox-owned resources.
	ManagedByValue = "kubox"
	// AnnotationExpiresAt carries the RFC3339 expiry timestamp.
	AnnotationExpiresAt = "kubox.dev/expires-at"
	// AnnotationSliceDir records where the slice was loaded from.
	AnnotationSliceDir = "kubox.dev/slice-dir"
)

type Environment struct {
	Name      string    `json:"name"`
	Namespace string    `json:"namespace"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`
	Services  []string  `json:"services"`
}

type Manager struct {
	Client       kubernetes.Interface
	restCfg      *rest.Config
	toolboxImage string
	waitFn       func(context.Context, string) error
	// probeFn checks that the app answers (HTTP when a healthcheck path is
	// known, TCP otherwise); overridable in tests.
	probeFn func(ctx context.Context, envName, host string, port int32, healthPath string) error
}

func NewManager(cs kubernetes.Interface, cfg *rest.Config, toolboxImage string) *Manager {
	if toolboxImage == "" {
		toolboxImage = slice.DefaultToolboxImage
	}
	m := &Manager{Client: cs, restCfg: cfg, toolboxImage: toolboxImage}
	m.waitFn = m.waitDeployments
	m.probeFn = m.defaultProbe
	return m
}

func (m *Manager) wait(ctx context.Context, ns string) error {
	if m.waitFn != nil {
		return m.waitFn(ctx, ns)
	}
	return m.waitDeployments(ctx, ns)
}

func (m *Manager) Create(ctx context.Context, s *slice.Slice, ttl time.Duration) (*Environment, error) {
	env, err := m.ensure(ctx, s, ttl)
	if err != nil {
		return nil, err
	}
	if err := m.wait(ctx, env.Namespace); err != nil {
		return nil, err
	}
	return env, nil
}

func (m *Manager) ensure(ctx context.Context, s *slice.Slice, ttl time.Duration) (*Environment, error) {
	ns := slice.EnvNamespace(s.Name)
	expires := time.Now().Add(ttl).UTC()
	createdAt := time.Now().UTC()
	resync := false

	if existing, err := m.Client.CoreV1().Namespaces().Get(ctx, ns, metav1.GetOptions{}); err == nil {
		switch {
		case existing.Status.Phase == corev1.NamespaceTerminating:
			if err := m.waitForNamespaceGone(ctx, ns); err != nil {
				return nil, fmt.Errorf("environment %q is still being deleted (namespace %s) — retry shortly", s.Name, ns)
			}
		case existing.Labels[LabelEnv] == s.Name:
			resync = true
			createdAt = existing.CreationTimestamp.Time.UTC()
			if err := m.refreshTTL(ctx, existing, expires); err != nil {
				return nil, err
			}
		default:
			return nil, fmt.Errorf("namespace %s already exists and is not a kubox environment", ns)
		}
	}

	if !resync {
		if err := m.createNamespace(ctx, s, ns, expires); err != nil {
			return nil, err
		}
	}
	if err := m.ensureQuota(ctx, s, ns); err != nil {
		return nil, err
	}
	if err := m.ensureNetworkPolicy(ctx, s, ns); err != nil {
		return nil, err
	}
	if err := m.ensureServiceAccount(ctx, ns); err != nil {
		return nil, err
	}
	if err := m.deployAll(ctx, s, ns); err != nil {
		return nil, err
	}

	status := "ready"
	if resync {
		status = "resynced"
	}
	return &Environment{
		Name:      s.Name,
		Namespace: ns,
		Status:    status,
		CreatedAt: createdAt,
		ExpiresAt: expires,
		Services:  m.serviceNames(s),
	}, nil
}

func (m *Manager) Delete(ctx context.Context, name string) error {
	ns := slice.EnvNamespace(name)
	err := m.Client.CoreV1().Namespaces().Delete(ctx, ns, metav1.DeleteOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}

	deadline := time.After(readyTimeout)
	for {
		_, err := m.Client.CoreV1().Namespaces().Get(ctx, ns, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("deleting %s: %w", ns, ctx.Err())
		case <-deadline:
			return nil
		case <-time.After(time.Second):
		}
	}
}

func (m *Manager) Get(ctx context.Context, name string) (*Environment, error) {
	ns := slice.EnvNamespace(name)
	obj, err := m.Client.CoreV1().Namespaces().Get(ctx, ns, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil, fmt.Errorf("environment %q not found", name)
	}
	if err != nil {
		return nil, err
	}
	return m.namespaceToEnv(ctx, obj)
}

func (m *Manager) List(ctx context.Context) ([]Environment, error) {
	list, err := m.Client.CoreV1().Namespaces().List(ctx, metav1.ListOptions{
		LabelSelector: LabelEnv,
	})
	if err != nil {
		return nil, err
	}
	envs := make([]Environment, 0, len(list.Items))
	for i := range list.Items {
		e, err := m.namespaceToEnv(ctx, &list.Items[i])
		if err != nil {
			continue
		}
		envs = append(envs, *e)
	}
	sort.Slice(envs, func(i, j int) bool { return envs[i].Name < envs[j].Name })
	return envs, nil
}

func (m *Manager) SweepExpired(ctx context.Context) ([]string, error) {
	list, err := m.Client.CoreV1().Namespaces().List(ctx, metav1.ListOptions{
		LabelSelector: LabelEnv,
	})
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	var deleted []string
	for i := range list.Items {
		ns := &list.Items[i]
		if ns.Status.Phase == corev1.NamespaceTerminating {
			continue
		}
		raw, ok := ns.Annotations[AnnotationExpiresAt]
		if !ok {
			continue
		}
		expires, err := time.Parse(time.RFC3339, raw)
		if err != nil || !now.After(expires) {
			continue
		}
		if err := m.Client.CoreV1().Namespaces().Delete(ctx, ns.Name, metav1.DeleteOptions{}); err == nil {
			deleted = append(deleted, ns.Labels[LabelEnv])
		}
	}
	return deleted, nil
}

func (m *Manager) waitForNamespaceGone(ctx context.Context, ns string) error {
	deadline := time.Now().Add(60 * time.Second)
	for {
		_, err := m.Client.CoreV1().Namespaces().Get(ctx, ns, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return nil
		}
		if err != nil {
			return err
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("namespace %s still terminating", ns)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

func (m *Manager) createNamespace(ctx context.Context, s *slice.Slice, ns string, expires time.Time) error {
	obj := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: ns,
			Labels: map[string]string{
				LabelEnv:       s.Name,
				LabelManagedBy: ManagedByValue,
				LabelComponent: "environment",
			},
			Annotations: map[string]string{
				AnnotationExpiresAt: expires.Format(time.RFC3339),
			},
		},
	}
	_, err := m.Client.CoreV1().Namespaces().Create(ctx, obj, metav1.CreateOptions{})
	if err != nil {
		return fmt.Errorf("create namespace %s: %w", ns, err)
	}
	return nil
}

func (m *Manager) namespaceToEnv(ctx context.Context, ns *corev1.Namespace) (*Environment, error) {
	env := &Environment{
		Name:      ns.Labels[LabelEnv],
		Namespace: ns.Name,
		CreatedAt: ns.CreationTimestamp.Time.UTC(),
	}
	if raw, ok := ns.Annotations[AnnotationExpiresAt]; ok {
		if t, err := time.Parse(time.RFC3339, raw); err == nil {
			env.ExpiresAt = t
		}
	}
	switch ns.Status.Phase {
	case corev1.NamespaceTerminating:
		env.Status = "deleting"
	default:
		env.Status = "ready"
	}

	svcs, err := m.Client.CoreV1().Services(ns.Name).List(ctx, metav1.ListOptions{
		LabelSelector: LabelEnv + "=" + env.Name,
	})
	if err == nil {
		for i := range svcs.Items {
			env.Services = append(env.Services, svcs.Items[i].Name)
		}
		sort.Strings(env.Services)
	}
	return env, nil
}

func (m *Manager) serviceNames(s *slice.Slice) []string {
	names := []string{s.AppServiceName()}
	for _, d := range s.Dependencies {
		names = append(names, d.Name)
	}
	return names
}
