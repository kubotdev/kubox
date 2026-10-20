package env

import (
	"context"
	"fmt"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	"github.com/kubotdev/kubox/internal/slice"
)

func (m *Manager) ensureQuota(ctx context.Context, s *slice.Slice, ns string) error {
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
	quotas := m.Client.CoreV1().ResourceQuotas(ns)
	existing, err := quotas.Get(ctx, obj.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		if _, err := quotas.Create(ctx, obj, metav1.CreateOptions{}); err != nil {
			return fmt.Errorf("create resourcequota: %w", err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("get resourcequota: %w", err)
	}
	updated := existing.DeepCopy()
	updated.Labels = obj.Labels
	updated.Spec.Hard = hard
	if _, err := quotas.Update(ctx, updated, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("update resourcequota: %w", err)
	}
	return nil
}

func (m *Manager) ensureNetworkPolicy(ctx context.Context, s *slice.Slice, ns string) error {
	policies := m.Client.NetworkingV1().NetworkPolicies(ns)
	existing, err := policies.Get(ctx, "kubox-default-deny", metav1.GetOptions{})
	if s.NetworkPolicy.Default != "deny-all" {
		if err == nil {
			if err := policies.Delete(ctx, existing.Name, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
				return fmt.Errorf("delete networkpolicy: %w", err)
			}
		} else if !apierrors.IsNotFound(err) {
			return fmt.Errorf("get networkpolicy: %w", err)
		}
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
	if apierrors.IsNotFound(err) {
		if _, err := policies.Create(ctx, obj, metav1.CreateOptions{}); err != nil {
			return fmt.Errorf("create networkpolicy: %w", err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("get networkpolicy: %w", err)
	}
	updated := existing.DeepCopy()
	updated.Labels = obj.Labels
	updated.Spec = obj.Spec
	if _, err := policies.Update(ctx, updated, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("update networkpolicy: %w", err)
	}
	return nil
}

func (m *Manager) ensureServiceAccount(ctx context.Context, ns string) error {
	sa := &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "kubox-toolbox",
			Namespace: ns,
		},
	}
	if _, err := m.Client.CoreV1().ServiceAccounts(ns).Create(ctx, sa, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
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
	if _, err := m.Client.RbacV1().RoleBindings(ns).Create(ctx, rb, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("create rolebinding: %w", err)
	}
	return nil
}

func (m *Manager) deployApp(ctx context.Context, s *slice.Slice, ns string) error {
	replicas := *s.App.Replicas
	if s.App.Scaling != nil {
		replicas = s.App.Scaling.Min
	}
	if err := m.deployService(ctx, deploySpec{
		envName:   s.Name,
		namespace: ns,
		name:      s.AppServiceName(),
		image:     s.App.Image,
		replicas:  replicas,
		port:      s.App.Port,
		env:       s.App.Env,
		res:       s.App.Resources,
		health:    s.App.HealthCheck,
		scaling:   s.App.Scaling,
	}); err != nil {
		return err
	}
	return m.ensureAppHPA(ctx, s, ns)
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
		volume:    d.Volume,
		recreate:  true,
	})
}

func (m *Manager) deployAll(ctx context.Context, s *slice.Slice, ns string) error {
	for _, d := range s.Dependencies {
		if err := m.deployDependency(ctx, s, d, ns); err != nil {
			return err
		}
	}
	if err := m.wait(ctx, ns); err != nil {
		return fmt.Errorf("waiting for dependencies: %w", err)
	}
	if err := m.deployApp(ctx, s, ns); err != nil {
		return err
	}
	return m.pruneServices(ctx, s, ns)
}

func (m *Manager) pruneServices(ctx context.Context, s *slice.Slice, ns string) error {
	want := make(map[string]bool, len(s.Dependencies)+1)
	for _, n := range m.serviceNames(s) {
		want[n] = true
	}
	selector := LabelEnv + "=" + s.Name

	deps, err := m.Client.AppsV1().Deployments(ns).List(ctx, metav1.ListOptions{LabelSelector: selector})
	if err != nil {
		return fmt.Errorf("list deployments: %w", err)
	}
	for i := range deps.Items {
		name := deps.Items[i].Name
		if want[name] {
			continue
		}
		if err := m.Client.AppsV1().Deployments(ns).Delete(ctx, name, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("prune deployment %s: %w", name, err)
		}
	}

	svcs, err := m.Client.CoreV1().Services(ns).List(ctx, metav1.ListOptions{LabelSelector: selector})
	if err != nil {
		return fmt.Errorf("list services: %w", err)
	}
	for i := range svcs.Items {
		name := svcs.Items[i].Name
		if want[name] {
			continue
		}
		if err := m.Client.CoreV1().Services(ns).Delete(ctx, name, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("prune service %s: %w", name, err)
		}
	}

	wantPVC := make(map[string]bool)
	for _, d := range s.Dependencies {
		if d.Volume != nil {
			wantPVC[d.Name+"-data"] = true
		}
	}
	pvcs, err := m.Client.CoreV1().PersistentVolumeClaims(ns).List(ctx, metav1.ListOptions{LabelSelector: selector})
	if err != nil {
		return fmt.Errorf("list pvcs: %w", err)
	}
	for i := range pvcs.Items {
		name := pvcs.Items[i].Name
		if wantPVC[name] {
			continue
		}
		if err := m.Client.CoreV1().PersistentVolumeClaims(ns).Delete(ctx, name, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("prune pvc %s: %w", name, err)
		}
	}
	return nil
}

func (m *Manager) ensureVolume(ctx context.Context, spec deploySpec, labels map[string]string) error {
	if spec.volume == nil {
		return nil
	}
	name := spec.name + "-data"
	pvcs := m.Client.CoreV1().PersistentVolumeClaims(spec.namespace)
	if _, err := pvcs.Get(ctx, name, metav1.GetOptions{}); err == nil {
		return nil
	} else if !apierrors.IsNotFound(err) {
		return fmt.Errorf("get pvc %s: %w", name, err)
	}
	obj := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: spec.namespace,
			Labels:    labels,
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceStorage: resource.MustParse(spec.volume.Size),
				},
			},
		},
	}
	if _, err := pvcs.Create(ctx, obj, metav1.CreateOptions{}); err != nil {
		return fmt.Errorf("create pvc %s: %w", name, err)
	}
	return nil
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
	volume    *slice.Volume
	recreate  bool
	health    *slice.HealthCheck
	scaling   *slice.Scaling
}

func (m *Manager) deployService(ctx context.Context, spec deploySpec) error {
	labels := commonLabels(spec.envName, spec.name)
	labels["app"] = spec.name

	if err := m.ensureVolume(ctx, spec, labels); err != nil {
		return err
	}

	dep := buildDeployment(spec, labels)
	deployments := m.Client.AppsV1().Deployments(spec.namespace)
	existingDep, err := deployments.Get(ctx, spec.name, metav1.GetOptions{})
	switch {
	case apierrors.IsNotFound(err):
		if _, err := deployments.Create(ctx, dep, metav1.CreateOptions{}); err != nil {
			return fmt.Errorf("create deployment %s: %w", spec.name, err)
		}
	case err != nil:
		return fmt.Errorf("get deployment %s: %w", spec.name, err)
	default:
		updated := existingDep.DeepCopy()
		updated.Labels = dep.Labels
		if spec.scaling == nil {
			updated.Spec.Replicas = dep.Spec.Replicas
		}
		updated.Spec.Selector = dep.Spec.Selector
		updated.Spec.Template = dep.Spec.Template
		if updated.Spec.Template.Annotations == nil {
			updated.Spec.Template.Annotations = map[string]string{}
		}
		updated.Spec.Template.Annotations[AnnotationRestartedAt] = time.Now().UTC().Format(time.RFC3339Nano)
		if _, err := deployments.Update(ctx, updated, metav1.UpdateOptions{}); err != nil {
			return fmt.Errorf("update deployment %s: %w", spec.name, err)
		}
	}

	svc := buildService(spec, labels)
	services := m.Client.CoreV1().Services(spec.namespace)
	existingSvc, err := services.Get(ctx, spec.name, metav1.GetOptions{})
	switch {
	case apierrors.IsNotFound(err):
		if _, err := services.Create(ctx, svc, metav1.CreateOptions{}); err != nil {
			return fmt.Errorf("create service %s: %w", spec.name, err)
		}
	case err != nil:
		return fmt.Errorf("get service %s: %w", spec.name, err)
	default:
		updated := existingSvc.DeepCopy()
		updated.Labels = svc.Labels
		updated.Spec.Selector = svc.Spec.Selector
		updated.Spec.Ports = svc.Spec.Ports
		if _, err := services.Update(ctx, updated, metav1.UpdateOptions{}); err != nil {
			return fmt.Errorf("update service %s: %w", spec.name, err)
		}
	}
	return nil
}

func buildDeployment(spec deploySpec, labels map[string]string) *appsv1.Deployment {
	podSpec := corev1.PodSpec{
		ServiceAccountName: "kubox-toolbox",
		Containers: []corev1.Container{{
			Name:      spec.name,
			Image:     spec.image,
			Ports:     []corev1.ContainerPort{{ContainerPort: int32(spec.port)}},
			Env:       envVars(spec.env),
			Resources: containerResources(spec.res),
		}},
	}
	if spec.volume != nil {
		podSpec.Volumes = []corev1.Volume{{
			Name: "data",
			VolumeSource: corev1.VolumeSource{
				PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
					ClaimName: spec.name + "-data",
				},
			},
		}}
		podSpec.Containers[0].VolumeMounts = []corev1.VolumeMount{{
			Name:      "data",
			MountPath: spec.volume.Path,
		}}
	}

	if spec.health != nil {
		probe := &corev1.Probe{
			ProbeHandler: corev1.ProbeHandler{
				HTTPGet: &corev1.HTTPGetAction{
					Path: spec.health.Path,
					Port: intstr.FromInt(spec.port),
				},
			},
			InitialDelaySeconds: int32(spec.health.InitialDelaySeconds),
			PeriodSeconds:       5,
			TimeoutSeconds:      3,
			FailureThreshold:    3,
		}
		podSpec.Containers[0].ReadinessProbe = probe
		liveness := probe.DeepCopy()
		liveness.InitialDelaySeconds += 10
		podSpec.Containers[0].LivenessProbe = liveness
	}

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
				Spec:       podSpec,
			},
		},
	}
	if spec.recreate {
		dep.Spec.Strategy = appsv1.DeploymentStrategy{
			Type: appsv1.RecreateDeploymentStrategyType,
		}
		dep.Spec.Template.Spec.TerminationGracePeriodSeconds = func() *int64 { s := int64(5); return &s }()
	}
	return dep
}

func buildService(spec deploySpec, labels map[string]string) *corev1.Service {
	return &corev1.Service{
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

func (m *Manager) ensureAppHPA(ctx context.Context, s *slice.Slice, ns string) error {
	name := s.AppServiceName()
	hpas := m.Client.AutoscalingV2().HorizontalPodAutoscalers(ns)
	if s.App.Scaling == nil {
		if err := hpas.Delete(ctx, name, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("delete hpa %s: %w", name, err)
		}
		return nil
	}
	min := int32(s.App.Scaling.Min)
	cpuPercent := int32(s.App.Scaling.CPUPercent)
	hpa := &autoscalingv2.HorizontalPodAutoscaler{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: ns,
			Labels:    commonLabels(s.Name, name),
		},
		Spec: autoscalingv2.HorizontalPodAutoscalerSpec{
			ScaleTargetRef: autoscalingv2.CrossVersionObjectReference{
				APIVersion: "apps/v1",
				Kind:       "Deployment",
				Name:       name,
			},
			MinReplicas: &min,
			MaxReplicas: int32(s.App.Scaling.Max),
			Metrics: []autoscalingv2.MetricSpec{{
				Type: autoscalingv2.ResourceMetricSourceType,
				Resource: &autoscalingv2.ResourceMetricSource{
					Name: corev1.ResourceCPU,
					Target: autoscalingv2.MetricTarget{
						Type:               autoscalingv2.UtilizationMetricType,
						AverageUtilization: &cpuPercent,
					},
				},
			}},
		},
	}
	existing, err := hpas.Get(ctx, name, metav1.GetOptions{})
	switch {
	case apierrors.IsNotFound(err):
		if _, err := hpas.Create(ctx, hpa, metav1.CreateOptions{}); err != nil {
			return fmt.Errorf("create hpa %s: %w", name, err)
		}
	case err != nil:
		return fmt.Errorf("get hpa %s: %w", name, err)
	default:
		updated := existing.DeepCopy()
		updated.Labels = hpa.Labels
		updated.Spec = hpa.Spec
		if _, err := hpas.Update(ctx, updated, metav1.UpdateOptions{}); err != nil {
			return fmt.Errorf("update hpa %s: %w", name, err)
		}
	}
	return nil
}
