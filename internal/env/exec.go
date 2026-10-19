package env

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/remotecommand"
	utilexec "k8s.io/client-go/util/exec"

	"github.com/kubotdev/kubox/internal/slice"
)

const toolboxName = "kubox-toolbox"

type ExecResult struct {
	ExitCode int
	Stdout   string
	Stderr   string
}

func (m *Manager) ExecToolbox(ctx context.Context, envName string, command []string) (*ExecResult, error) {
	ns := slice.EnvNamespace(envName)
	if err := m.ensureToolbox(ctx, envName, ns); err != nil {
		return nil, err
	}
	return m.execInPod(ctx, ns, toolboxName, toolboxName, command)
}

func (m *Manager) ExecOnce(ctx context.Context, envName, image string, command []string, mounts []Mount) (*ExecResult, error) {
	ns := slice.EnvNamespace(envName)
	name := fmt.Sprintf("kubox-run-%d", time.Now().UnixNano()%1e9)

	var volMounts []corev1.VolumeMount
	var volumes []corev1.Volume
	for _, mt := range mounts {
		volMounts = append(volMounts, corev1.VolumeMount{Name: mt.Name, MountPath: mt.MountPath})
		if mt.ConfigMapName != "" {
			volumes = append(volumes, corev1.Volume{
				Name: mt.Name,
				VolumeSource: corev1.VolumeSource{
					ConfigMap: &corev1.ConfigMapVolumeSource{
						LocalObjectReference: corev1.LocalObjectReference{Name: mt.ConfigMapName},
					},
				},
			})
		}
	}

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: ns,
			Labels:    commonLabels(envName, "runner"),
		},
		Spec: corev1.PodSpec{
			RestartPolicy:      corev1.RestartPolicyNever,
			ServiceAccountName: "kubox-toolbox",
			Containers: []corev1.Container{{
				Name:         "run",
				Image:        image,
				Command:      command,
				VolumeMounts: volMounts,
				Resources: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("50m"),
						corev1.ResourceMemory: resource.MustParse("64Mi"),
					},
				},
			}},
			Volumes: volumes,
		},
	}
	if _, err := m.Client.CoreV1().Pods(ns).Create(ctx, pod, metav1.CreateOptions{}); err != nil {
		return nil, fmt.Errorf("create runner pod: %w", err)
	}
	defer func() {
		_ = m.Client.CoreV1().Pods(ns).Delete(context.WithoutCancel(ctx), name, metav1.DeleteOptions{})
	}()

	deadline := time.Now().Add(10 * time.Minute)
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("runner pod %s timed out", name)
		}
		p, err := m.Client.CoreV1().Pods(ns).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return nil, err
		}
		switch p.Status.Phase {
		case corev1.PodSucceeded, corev1.PodFailed:
			return m.collectPodResult(ctx, ns, name, "run", p)
		}
		if reason, msg := podStartupFailure(p); reason != "" {
			return nil, fmt.Errorf("runner pod failed to start: %s: %s", reason, msg)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}

type Mount struct {
	Name          string
	MountPath     string
	ConfigMapName string
}

func (m *Manager) collectPodResult(ctx context.Context, ns, name, container string, pod *corev1.Pod) (*ExecResult, error) {
	res := &ExecResult{}
	if pod.Status.Phase == corev1.PodFailed {
		for _, cs := range pod.Status.ContainerStatuses {
			if cs.State.Terminated != nil {
				res.ExitCode = int(cs.State.Terminated.ExitCode)
				break
			}
		}
		if res.ExitCode == 0 {
			res.ExitCode = 1
		}
	}

	logs, err := m.Client.CoreV1().Pods(ns).GetLogs(name, &corev1.PodLogOptions{}).Do(ctx).Raw()
	if err == nil {
		res.Stdout = string(logs)
	}

	if out, err := m.execInPod(ctx, ns, name, container, []string{"cat", "/tmp/results.json"}); err == nil && out.Stdout != "" {
		res.Stdout = out.Stdout
	}
	return res, nil
}

func (m *Manager) ensureToolbox(ctx context.Context, envName, ns string) error {
	pod, err := m.Client.CoreV1().Pods(ns).Get(ctx, toolboxName, metav1.GetOptions{})
	if err == nil && pod.Status.Phase == corev1.PodRunning {
		return nil
	}

	if err == nil {
		_ = m.Client.CoreV1().Pods(ns).Delete(ctx, toolboxName, metav1.DeleteOptions{})
		deadline := time.Now().Add(30 * time.Second)
		for time.Now().Before(deadline) {
			if _, gerr := m.Client.CoreV1().Pods(ns).Get(ctx, toolboxName, metav1.GetOptions{}); gerr != nil {
				break
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(500 * time.Millisecond):
			}
		}
	}

	pod = &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      toolboxName,
			Namespace: ns,
			Labels:    commonLabels(envName, "toolbox"),
		},
		Spec: corev1.PodSpec{
			RestartPolicy:      corev1.RestartPolicyAlways,
			ServiceAccountName: "kubox-toolbox",
			Containers: []corev1.Container{{
				Name:    toolboxName,
				Image:   m.toolboxImage,
				Command: []string{"sh", "-c", "while true; do sleep 3600; done"},
				Resources: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("50m"),
						corev1.ResourceMemory: resource.MustParse("64Mi"),
					},
				},
			}},
		},
	}
	if _, err := m.Client.CoreV1().Pods(ns).Create(ctx, pod, metav1.CreateOptions{}); err != nil {
		return fmt.Errorf("create toolbox pod: %w", err)
	}

	deadline := time.Now().Add(2 * time.Minute)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("toolbox pod did not become ready in time")
		}
		p, err := m.Client.CoreV1().Pods(ns).Get(ctx, toolboxName, metav1.GetOptions{})
		if err == nil && p.Status.Phase == corev1.PodRunning {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(1 * time.Second):
		}
	}
}

func (m *Manager) execInPod(ctx context.Context, ns, podName, container string, command []string) (*ExecResult, error) {
	if m.restCfg == nil {
		return nil, fmt.Errorf("no rest config available for exec")
	}
	req := m.Client.CoreV1().RESTClient().Post().
		Resource("pods").
		Name(podName).
		Namespace(ns).
		SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{
			Container: container,
			Command:   command,
			Stdout:    true,
			Stderr:    true,
		}, scheme.ParameterCodec)

	exec, err := remotecommand.NewSPDYExecutor(m.restCfg, http.MethodPost, req.URL())
	if err != nil {
		return nil, fmt.Errorf("create executor: %w", err)
	}

	var stdout, stderr bytes.Buffer
	streamErr := exec.StreamWithContext(ctx, remotecommand.StreamOptions{
		Stdout: &stdout,
		Stderr: &stderr,
	})
	res := &ExecResult{
		Stdout: stdout.String(),
		Stderr: stderr.String(),
	}
	if streamErr != nil {
		if exitErr, ok := streamErr.(utilexec.ExitError); ok {
			res.ExitCode = exitErr.ExitStatus()
			return res, nil
		}
		return nil, fmt.Errorf("exec: %w", streamErr)
	}
	return res, nil
}
