package env

import (
	"context"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	"github.com/kubotdev/kubox/internal/slice"
)

func (m *Manager) applyQuota(ctx context.Context, s *slice.Slice, ns string) error {
	hard := corev1.ResourceList{
		corev1.ResourceCPU:    resource.MustParse(s.Quotas.CPU),
		corev1.ResourceMemory: resource.MustParse(s.Quotas.Memory),
		corev1.ResourcePods:   resource.MustParse(s.Quotas.Pods),
	}
	obj := &corev1.ResourceQuota{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "kubox-quota",
			Namespace: ns,
			Labels:    commonLabels(s.Name, "quota"),
		},
		Spec: corev1.ResourceQuotaSpec{Hard: hard},
	}
	_, err := m.Client.CoreV1().ResourceQuotas(ns).Create(ctx, obj, metav1.CreateOptions{})
	if err != nil {
		return fmt.Errorf("create resourcequota: %w", err)
	}
	return nil
}

func (m *Manager) applyNetworkPolicy(ctx context.Context, s *slice.Slice, ns string) error {
	if s.NetworkPolicy.Default != "deny-all" {
		return nil
	}

	policyTypes := []networkingv1.PolicyType{networkingv1.PolicyTypeIngress, networkingv1.PolicyTypeEgress}
	obj := &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "kubox-default-deny",
			Namespace: ns,
			Labels:    commonLabels(s.Name, "networkpolicy"),
		},
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{}, // all pods in ns
			PolicyTypes: policyTypes,
			Ingress: []networkingv1.NetworkPolicyIngressRule{{
				From: []networkingv1.NetworkPolicyPeer{{
					PodSelector: &metav1.LabelSelector{}, // same namespace
				}},
			}},
			Egress: []networkingv1.NetworkPolicyEgressRule{{
				To: []networkingv1.NetworkPolicyPeer{{
					PodSelector: &metav1.LabelSelector{}, // same namespace
				}},
			}, {
				// DNS to kube-system.
				To: []networkingv1.NetworkPolicyPeer{{
					NamespaceSelector: &metav1.LabelSelector{
						MatchLabels: map[string]string{"kubernetes.io/metadata.name": "kube-system"},
					},
				}},
				Ports: []networkingv1.NetworkPolicyPort{{
					Protocol: func() *corev1.Protocol { p := corev1.ProtocolUDP; return &p }(),
					Port:     func() *intstr.IntOrString { p := intstr.FromInt(53); return &p }(),
				}, {
					Protocol: func() *corev1.Protocol { p := corev1.ProtocolTCP; return &p }(),
					Port:     func() *intstr.IntOrString { p := intstr.FromInt(53); return &p }(),
				}},
			}, {
				Ports: []networkingv1.NetworkPolicyPort{{
					Protocol: func() *corev1.Protocol { p := corev1.ProtocolTCP; return &p }(),
					Port:     func() *intstr.IntOrString { p := intstr.FromInt(443); return &p }(),
				}, {
					Protocol: func() *corev1.Protocol { p := corev1.ProtocolTCP; return &p }(),
					Port:     func() *intstr.IntOrString { p := intstr.FromInt(6443); return &p }(),
				}},
			}},
		},
	}
	_, err := m.Client.NetworkingV1().NetworkPolicies(ns).Create(ctx, obj, metav1.CreateOptions{})
	if err != nil {
		return fmt.Errorf("create networkpolicy: %w", err)
	}
	return nil
}

func (m *Manager) applyServiceAccount(ctx context.Context, ns string) error {
	obj := &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "kubox-toolbox",
			Namespace: ns,
		},
	}
	if _, err := m.Client.CoreV1().ServiceAccounts(ns).Create(ctx, obj, metav1.CreateOptions{}); err != nil {
		return fmt.Errorf("create serviceaccount: %w", err)
	}

	rb := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "kubox-toolbox-edit",
			Namespace: ns,
			Labels:    commonLabels("", "rbac"),
		},
		RoleRef: rbacv1.RoleRef{
			APIGroup: rbacv1.GroupName,
			Kind:     "ClusterRole",
			Name:     "edit",
		},
		Subjects: []rbacv1.Subject{{
			Kind:      rbacv1.ServiceAccountKind,
			Name:      "kubox-toolbox",
			Namespace: ns,
		}},
	}
	if _, err := m.Client.RbacV1().RoleBindings(ns).Create(ctx, rb, metav1.CreateOptions{}); err != nil {
		return fmt.Errorf("create rolebinding: %w", err)
	}
	return nil
}

func (m *Manager) deployApp(ctx context.Context, s *slice.Slice, ns string) error {
	return m.deployService(ctx, deploySpec{
		envName:   s.Name,
		namespace: ns,
		name:      s.AppServiceName(),
		image:     s.App.Image,
		replicas:  *s.App.Replicas,
		port:      s.App.Port,
		env:       s.App.Env,
		res:       s.App.Resources,
	})
}

func (m *Manager) deployDependency(ctx context.Context, s *slice.Slice, d slice.Dependency, ns string) error {
	return m.deployService(ctx, deploySpec{
		envName:   s.Name,
		namespace: ns,
		name:      d.Name,
		image:     d.Image,
		replicas:  1,
		port:      d.Port,
		env:       d.Env,
		res:       d.Resources,
	})
}

type deploySpec struct {
	envName   string
	namespace string
	name      string
	image     string
	replicas  int
	port      int
	env       map[string]string
	res       slice.Resources
}

func (m *Manager) deployService(ctx context.Context, spec deploySpec) error {
	labels := commonLabels(spec.envName, spec.name)
	labels["app"] = spec.name

	// Deployment
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      spec.name,
			Namespace: spec.namespace,
			Labels:    labels,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: int32ptr(int32(spec.replicas)),
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": spec.name}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec: corev1.PodSpec{
					ServiceAccountName: "kubox-toolbox",
					Containers: []corev1.Container{{
						Name:      spec.name,
						Image:     spec.image,
						Ports:     []corev1.ContainerPort{{ContainerPort: int32(spec.port)}},
						Env:       envVars(spec.env),
						Resources: containerResources(spec.res),
					}},
				},
			},
		},
	}
	if _, err := m.Client.AppsV1().Deployments(spec.namespace).Create(ctx, dep, metav1.CreateOptions{}); err != nil {
		return fmt.Errorf("create deployment %s: %w", spec.name, err)
	}

	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      spec.name,
			Namespace: spec.namespace,
			Labels:    labels,
		},
		Spec: corev1.ServiceSpec{
			Selector: map[string]string{"app": spec.name},
			Ports: []corev1.ServicePort{{
				Name:       "http",
				Port:       int32(spec.port),
				TargetPort: intstr.FromInt(spec.port),
			}},
		},
	}
	if _, err := m.Client.CoreV1().Services(spec.namespace).Create(ctx, svc, metav1.CreateOptions{}); err != nil {
		return fmt.Errorf("create service %s: %w", spec.name, err)
	}
	return nil
}

func commonLabels(envName, component string) map[string]string {
	return map[string]string{
		LabelEnv:       envName,
		LabelComponent: component,
		LabelManagedBy: ManagedByValue,
	}
}

func envVars(m map[string]string) []corev1.EnvVar {
	if len(m) == 0 {
		return nil
	}
	out := make([]corev1.EnvVar, 0, len(m))
	for k, v := range m {
		out = append(out, corev1.EnvVar{Name: k, Value: v})
	}
	return out
}

func containerResources(r slice.Resources) corev1.ResourceRequirements {
	req := corev1.ResourceList{}
	if r.RequestsCPU != "" {
		req[corev1.ResourceCPU] = resource.MustParse(r.RequestsCPU)
	}
	if r.RequestsMemory != "" {
		req[corev1.ResourceMemory] = resource.MustParse(r.RequestsMemory)
	}
	out := corev1.ResourceRequirements{Requests: req}
	if r.LimitsCPU != "" || r.LimitsMemory != "" {
		lim := corev1.ResourceList{}
		if r.LimitsCPU != "" {
			lim[corev1.ResourceCPU] = resource.MustParse(r.LimitsCPU)
		}
		if r.LimitsMemory != "" {
			lim[corev1.ResourceMemory] = resource.MustParse(r.LimitsMemory)
		}
		out.Limits = lim
	}
	return out
}

func int32ptr(i int32) *int32 { return &i }
