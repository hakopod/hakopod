package cluster

import (
	"fmt"
	"strings"
	"time"

	"github.com/hakopod/hakopod/internal/sandbox"
	"github.com/hakopod/hakopod/internal/sessionguard"
	"github.com/hakopod/hakopod/internal/spec"
	corev1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func (c *Client) sandboxPod(t Target, r sandbox.Record, s spec.Service, b RuntimeProfileBinding) *corev1.Pod {
	d := deployment(t, r.Service, s, time.Minute)
	p := d.Spec.Template.Spec
	p.RuntimeClassName = ptr(b.RuntimeClass)
	p.Overhead = b.sessionOverhead.DeepCopy()
	p.RestartPolicy = corev1.RestartPolicyNever
	p.ShareProcessNamespace = ptr(false)
	p.TerminationGracePeriodSeconds = ptr(int64(5))
	deadline := int64(time.Until(r.ExpiresAt).Seconds())
	if deadline < 1 {
		deadline = 1
	}
	p.ActiveDeadlineSeconds = ptr(deadline)
	p.DNSPolicy = corev1.DNSNone
	p.DNSConfig = &corev1.PodDNSConfig{Nameservers: []string{"127.0.0.1"}}
	p.SchedulerName = corev1.DefaultSchedulerName
	if s.RegistryCredential != "" {
		p.ImagePullSecrets = []corev1.LocalObjectReference{{Name: sandboxRegistrySecret}}
	}
	app := &p.Containers[0]
	app.Command = append(append([]string{sessionguard.Path, "run", "--"}, s.Command...), s.Args...)
	app.Args = nil
	app.Env = append(app.Env, corev1.EnvVar{Name: "HAKOPOD_POD_UID", ValueFrom: &corev1.EnvVarSource{FieldRef: &corev1.ObjectFieldSelector{APIVersion: "v1", FieldPath: "metadata.uid"}}}, corev1.EnvVar{Name: "HAKOPOD_SESSION_GENERATION", Value: r.Generation})
	app.StartupProbe = &corev1.Probe{ProbeHandler: corev1.ProbeHandler{Exec: &corev1.ExecAction{Command: append([]string{sessionguard.Path, "run", "--"}, s.Session.ReadyCommand...)}}, PeriodSeconds: 5, TimeoutSeconds: 2, FailureThreshold: 24, SuccessThreshold: 1}
	app.TerminationMessagePath = "/dev/termination-log"
	app.TerminationMessagePolicy = corev1.TerminationMessageReadFile
	p.Volumes = append(p.Volumes, corev1.Volume{Name: "session-guard", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{Medium: corev1.StorageMediumMemory, SizeLimit: ptr(resource.MustParse("32Mi"))}}})
	app.VolumeMounts = append(app.VolumeMounts, corev1.VolumeMount{Name: "session-guard", MountPath: "/run/hakopod-session", ReadOnly: true})
	p.InitContainers = []corev1.Container{{Name: "install-guard", Image: c.options.SessionGuardImage, ImagePullPolicy: corev1.PullAlways, Command: []string{"/hakopod-session-guard", "install"}, SecurityContext: &corev1.SecurityContext{AllowPrivilegeEscalation: ptr(false), ReadOnlyRootFilesystem: ptr(true), Capabilities: &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}}}, Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: *resource.NewMilliQuantity(sessionGuardCPURequestMillis, resource.DecimalSI), corev1.ResourceMemory: *resource.NewQuantity(sessionGuardMemoryRequestBytes, resource.BinarySI)}, Limits: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("100m"), corev1.ResourceMemory: resource.MustParse("64Mi")}}, VolumeMounts: []corev1.VolumeMount{{Name: "session-guard", MountPath: "/run/hakopod-session"}}, TerminationMessagePath: "/dev/termination-log", TerminationMessagePolicy: corev1.TerminationMessageReadFile}}
	labels := sandboxLabels(t, r)
	if s.RegistryCredential != "" {
		labels[registryScopeLabel] = RegistryScope(t.Project, t.Environment, s.RegistryCredential)
	}
	return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: sandboxPodName, Namespace: SandboxNamespace(r.ID), Labels: labels, Annotations: sandboxAnnotations(r, s)}, Spec: p}
}

func (c *Client) sandboxState(t Target, r sandbox.Record, s spec.Service, b RuntimeProfileBinding, pod *corev1.Pod, state sandbox.RuntimeState) (sandbox.RuntimeState, error) {
	if err := sandboxOwned(pod, t, r); err != nil {
		return state, err
	}
	if pod.Name != sandboxPodName || pod.Namespace != SandboxNamespace(r.ID) || pod.UID == "" || r.PodUID != "" && string(pod.UID) != r.PodUID {
		return state, fmt.Errorf("session Pod identity changed")
	}
	state.PodUID = string(pod.UID)
	if pod.DeletionTimestamp != nil || pod.Status.Phase == corev1.PodFailed || pod.Status.Phase == corev1.PodSucceeded {
		return state, fmt.Errorf("session kernel stopped")
	}
	if err := c.validateSandboxPod(t, r, s, b, pod); err != nil {
		return state, err
	}
	for _, status := range pod.Status.InitContainerStatuses {
		if status.Name != "install-guard" || status.RestartCount != 0 || status.State.Terminated != nil && status.State.Terminated.ExitCode != 0 {
			return state, fmt.Errorf("session guard initialization failed")
		}
	}
	if len(pod.Status.ContainerStatuses) > 1 {
		return state, fmt.Errorf("session contains an unexpected container status")
	}
	if len(pod.Status.ContainerStatuses) == 0 {
		return state, nil
	}
	status := pod.Status.ContainerStatuses[0]
	if status.Name != "app" || status.RestartCount != 0 || status.State.Terminated != nil {
		return state, fmt.Errorf("session kernel generation ended")
	}
	if status.ContainerID != "" {
		if len(status.ContainerID) > 512 || strings.ContainsAny(status.ContainerID, "\r\n\x00") || r.ContainerID != "" && r.ContainerID != status.ContainerID {
			return state, fmt.Errorf("session container identity changed")
		}
		state.ContainerID = status.ContainerID
	}
	if status.ImageID != "" {
		if len(status.ImageID) > 1024 || strings.ContainsAny(status.ImageID, "\r\n\x00") || r.ImageID != "" && r.ImageID != status.ImageID {
			return state, fmt.Errorf("session image identity changed")
		}
		state.ImageID = status.ImageID
	}
	if r.ContainerID != "" && state.ContainerID == "" || r.ImageID != "" && state.ImageID == "" {
		return state, fmt.Errorf("recorded session container identity is missing")
	}
	state.Ready = pod.Status.Phase == corev1.PodRunning && status.State.Running != nil && status.Started != nil && *status.Started && status.Ready && state.ContainerID != "" && state.ImageID != ""
	return state, nil
}
func (c *Client) validateSandboxPod(t Target, r sandbox.Record, s spec.Service, b RuntimeProfileBinding, pod *corev1.Pod) error {
	want := c.sandboxPod(t, r, s, b)
	a, e := pod.Spec, want.Spec
	for key, value := range want.Labels {
		if pod.Labels[key] != value {
			return fmt.Errorf("session Pod labels changed")
		}
	}
	for key, value := range want.Annotations {
		if pod.Annotations[key] != value {
			return fmt.Errorf("session template identity changed")
		}
	}
	if a.RestartPolicy != corev1.RestartPolicyNever || a.HostNetwork || a.HostPID || a.HostIPC || len(a.EphemeralContainers) > 0 || !apiequality.Semantic.DeepEqual(a.ImagePullSecrets, e.ImagePullSecrets) || a.ServiceAccountName != "" && a.ServiceAccountName != "default" || a.AutomountServiceAccountToken == nil || *a.AutomountServiceAccountToken || a.ShareProcessNamespace == nil || *a.ShareProcessNamespace || a.EnableServiceLinks == nil || *a.EnableServiceLinks {
		return fmt.Errorf("session Pod isolation changed")
	}
	if !apiequality.Semantic.DeepEqual(a.Overhead, e.Overhead) || !apiequality.Semantic.DeepEqual(a.RuntimeClassName, e.RuntimeClassName) || !apiequality.Semantic.DeepEqual(a.SecurityContext, e.SecurityContext) || !apiequality.Semantic.DeepEqual(a.Containers, e.Containers) || !apiequality.Semantic.DeepEqual(a.InitContainers, e.InitContainers) || !apiequality.Semantic.DeepEqual(a.Volumes, e.Volumes) || a.DNSPolicy != e.DNSPolicy || !apiequality.Semantic.DeepEqual(a.DNSConfig, e.DNSConfig) || !apiequality.Semantic.DeepEqual(a.TerminationGracePeriodSeconds, e.TerminationGracePeriodSeconds) {
		return fmt.Errorf("session Pod differs from its immutable template")
	}
	if a.ActiveDeadlineSeconds == nil || *a.ActiveDeadlineSeconds < 1 || *a.ActiveDeadlineSeconds > s.Session.LifetimeSeconds {
		return fmt.Errorf("session Pod lifetime is invalid")
	}
	if !pod.CreationTimestamp.IsZero() && pod.CreationTimestamp.Add(time.Duration(*a.ActiveDeadlineSeconds)*time.Second).After(r.ExpiresAt.Add(time.Second)) {
		return fmt.Errorf("session Pod deadline exceeds its receipt expiry")
	}
	return nil
}
