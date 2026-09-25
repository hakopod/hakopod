package cluster

import (
	"context"
	_ "embed"
	"fmt"
	"time"

	"github.com/hakopod/hakopod/internal/spec"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
)

const ActionsRuntime = "hakopod-actions"
const ActionsDaemonImage = "docker.io/library/docker:29.8.1-dind@sha256:3f3c01aaaebf7cce837356b688b7c059a4749f10bd7660dec7c58fc454a283f0"

//go:embed actions-daemon.sh
var actionsDaemon string

func (c *Client) ConfigureActions(reconcile func(context.Context, Target) error, observe func(context.Context, Target, string, spec.Service) (ServiceStatus, error)) {
	c.actionsReconcile = reconcile
	c.actionsObserve = observe
}
func (c *Client) ActionsAvailable(ctx context.Context) error {
	r, err := c.kube.NodeV1().RuntimeClasses().Get(ctx, ActionsRuntime, metav1.GetOptions{})
	if err != nil || r.Handler != ActionsRuntime || r.Labels[managedBy] != "hakopod" || r.Scheduling == nil || r.Scheduling.NodeSelector["hakopod.io/actions-runtime"] != "ready" {
		return fmt.Errorf("Managed Actions sandbox is not installed; enable it in the installation's runtime setup")
	}
	nodes, e := c.kube.CoreV1().Nodes().List(ctx, metav1.ListOptions{LabelSelector: "hakopod.io/actions-runtime=ready", Limit: 500})
	if e != nil {
		return fmt.Errorf("Managed Actions node readiness could not be checked")
	}
	for _, node := range nodes.Items {
		if actionsNodeReady(node) {
			return nil
		}
	}
	return fmt.Errorf("No ready node has the Managed Actions sandbox installed")
}
func actionsNodeReady(n corev1.Node) bool {
	if n.Spec.Unschedulable {
		return false
	}
	for _, c := range n.Status.Conditions {
		if c.Type == corev1.NodeReady && c.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}
func (c *Client) actionsCredential(ctx context.Context, t Target, name string) (string, error) {
	s, err := c.GetPlatformSecret(ctx, workloadSecretName(t.Project, t.Environment, t.Spec.Name, name))
	if err != nil || s.Labels["hakopod.io/secret-scope"] != secretScope(t.Project, t.Environment, t.Spec.Name) || s.Labels["hakopod.io/secret-name"] != name {
		return "", fmt.Errorf("Managed Actions credential is unavailable in this application")
	}
	return string(s.Data["value"]), nil
}
func (c *Client) ActionsCredential(ctx context.Context, t Target, name string) (string, error) {
	return c.actionsCredential(ctx, t, name)
}

// Resources are split within the declared per-slot budget, including the
// native Docker sidecar. No host path, socket, device or service-account token.
func actionsResources(s spec.Service, numerator int64) corev1.ResourceRequirements {
	p := spec.EffectiveResources(s)
	part := func(v string, cpu bool) resource.Quantity {
		q := resource.MustParse(v)
		if cpu {
			return *resource.NewMilliQuantity(max(1, (q.MilliValue()-100)*numerator/4), resource.DecimalSI)
		}
		return *resource.NewQuantity(max(1, (q.Value()-(512<<20))*numerator/4), resource.BinarySI)
	}
	return corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: part(p.CPURequest, true), corev1.ResourceMemory: part(p.MemoryRequest, false), corev1.ResourceEphemeralStorage: resource.MustParse("256Mi")}, Limits: corev1.ResourceList{corev1.ResourceCPU: part(p.CPULimit, true), corev1.ResourceMemory: part(p.MemoryLimit, false), corev1.ResourceEphemeralStorage: resource.MustParse("2Gi")}}
}

func actionsPod(t Target, service, id string, s spec.Service) *corev1.Pod {
	name := "actions-" + id
	safe := &corev1.SecurityContext{RunAsUser: ptr(int64(1001)), RunAsGroup: ptr(int64(1001)), RunAsNonRoot: ptr(true), AllowPrivilegeEscalation: ptr(false), Capabilities: &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}}}
	mounts := []corev1.VolumeMount{{Name: "runner", MountPath: "/home/runner"}, {Name: "socket", MountPath: "/var/run/docker"}}
	copyResources := actionsResources(s, 4)
	copyResources.Limits[corev1.ResourceEphemeralStorage] = resource.MustParse("4Gi")
	p := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: Namespace(t.ApplicationID), Labels: labelsFor(t, service)}, Spec: corev1.PodSpec{
		RuntimeClassName: ptr(ActionsRuntime), AutomountServiceAccountToken: ptr(false), EnableServiceLinks: ptr(false), RestartPolicy: corev1.RestartPolicyNever, ActiveDeadlineSeconds: ptr(s.Actions.TimeoutMinutes * 60), TerminationGracePeriodSeconds: ptr(int64(30)),
		SecurityContext: &corev1.PodSecurityContext{FSGroup: ptr(int64(1001)), SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault}},
		InitContainers: []corev1.Container{
			{Name: "prepare", Image: spec.ActionsRunnerImage, Command: []string{"sh", "-c", "cp -a /home/runner/. /runner/"}, SecurityContext: safe, Resources: copyResources, VolumeMounts: []corev1.VolumeMount{{Name: "runner", MountPath: "/runner"}}},
			{Name: "docker", Image: ActionsDaemonImage, Command: []string{"sh", "-c", actionsDaemon}, RestartPolicy: ptr(corev1.ContainerRestartPolicyAlways), Resources: actionsResources(s, 3),
				SecurityContext: &corev1.SecurityContext{Privileged: ptr(false), RunAsUser: ptr(int64(0)), Capabilities: &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}, Add: []corev1.Capability{"AUDIT_WRITE", "CHOWN", "DAC_OVERRIDE", "FOWNER", "FSETID", "KILL", "MKNOD", "NET_BIND_SERVICE", "NET_ADMIN", "NET_RAW", "SETFCAP", "SETGID", "SETPCAP", "SETUID", "SYS_ADMIN", "SYS_CHROOT", "SYS_PTRACE"}}},
				Env:             []corev1.EnvVar{{Name: "DOCKER_HOST", Value: "unix:///var/run/docker/docker.sock"}},
				StartupProbe:    &corev1.Probe{ProbeHandler: corev1.ProbeHandler{Exec: &corev1.ExecAction{Command: []string{"docker", "info"}}}, PeriodSeconds: 2, FailureThreshold: 60, TimeoutSeconds: 2},
				VolumeMounts:    append(append([]corev1.VolumeMount{}, mounts...), corev1.VolumeMount{Name: "docker", MountPath: "/var/lib/docker"})},
		},
		Containers: []corev1.Container{{Name: service, Image: spec.ActionsRunnerImage, WorkingDir: "/home/runner", Command: []string{"sh", "-c", "exec ./run.sh --jitconfig \"$(cat /run/hakopod-jit/config)\""}, SecurityContext: safe, Resources: actionsResources(s, 1), Env: []corev1.EnvVar{{Name: "DOCKER_HOST", Value: "unix:///var/run/docker/docker.sock"}, {Name: "ACTIONS_RUNNER_PRINT_LOG_TO_STDOUT", Value: "1"}}, VolumeMounts: append(append([]corev1.VolumeMount{}, mounts...), corev1.VolumeMount{Name: "jit", MountPath: "/run/hakopod-jit", ReadOnly: true})}},
		Volumes:    []corev1.Volume{{Name: "runner", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{SizeLimit: ptr(resource.MustParse("2Gi"))}}}, {Name: "socket", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{Medium: corev1.StorageMediumMemory, SizeLimit: ptr(resource.MustParse("1Mi"))}}}, {Name: "docker", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{Medium: corev1.StorageMediumMemory, SizeLimit: ptr(resource.MustParse("2Gi"))}}}, {Name: "jit", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: name, DefaultMode: ptr(int32(0440))}}}},
	}}
	if s.Architecture != "" {
		p.Spec.NodeSelector = map[string]string{"kubernetes.io/arch": s.Architecture}
	}
	if s.NodeName != "" {
		p.Spec.NodeSelector = map[string]string{"kubernetes.io/hostname": s.NodeName}
		if s.Architecture != "" {
			p.Spec.NodeSelector["kubernetes.io/arch"] = s.Architecture
		}
	}
	return p
}

func (c *Client) SaveActionsConfig(ctx context.Context, t Target, service, id, config string) error {
	if len(config) < 1 || len(config) > 128<<10 {
		return fmt.Errorf("invalid single-job configuration")
	}
	if err := beforeStep(ctx, t); err != nil {
		return err
	}
	wanted := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "actions-" + id, Namespace: Namespace(t.ApplicationID), Labels: labelsFor(t, service)}, Immutable: ptr(true), Data: map[string][]byte{"config": []byte(config)}}
	_, err := c.kube.CoreV1().Secrets(wanted.Namespace).Create(ctx, wanted, metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		current, e := c.kube.CoreV1().Secrets(wanted.Namespace).Get(ctx, wanted.Name, metav1.GetOptions{})
		if e != nil {
			return e
		}
		return owned(current, t)
	}
	return err
}
func (c *Client) StartActionsPod(ctx context.Context, t Target, service, id string, s spec.Service) error {
	if err := c.ActionsAvailable(ctx); err != nil {
		return err
	}
	p := actionsPod(t, service, id, s)
	policy, err := c.workloadPolicy(ctx, t)
	if err != nil {
		return err
	}
	if policy != nil {
		copy := *policy
		copy.RuntimeClass = ActionsRuntime
		copy.MemoryRequest = ""
		applyWorkloadPolicy(&copy, &p.Spec)
	}
	p.Spec.NodeSelector = mergeActionsSelector(p.Spec.NodeSelector)
	nodes, err := c.kube.CoreV1().Nodes().List(ctx, metav1.ListOptions{LabelSelector: labels.SelectorFromSet(p.Spec.NodeSelector).String(), Limit: 500})
	if err != nil {
		return fmt.Errorf("Managed Actions placement could not be checked")
	}
	ready := false
	for _, n := range nodes.Items {
		ready = ready || actionsNodeReady(n)
	}
	if !ready {
		return fmt.Errorf("No ready Managed Actions node matches this pool's placement")
	}
	if err = beforeStep(ctx, t); err != nil {
		return err
	}
	secret, err := c.kube.CoreV1().Secrets(p.Namespace).Get(ctx, p.Name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	if err = owned(secret, t); err != nil {
		return err
	}
	_, err = c.kube.CoreV1().Pods(p.Namespace).Create(ctx, p, metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		current, e := c.kube.CoreV1().Pods(p.Namespace).Get(ctx, p.Name, metav1.GetOptions{})
		if e != nil {
			return e
		}
		return owned(current, t)
	}
	return err
}
func (c *Client) ActionsPodPhase(ctx context.Context, t Target, id string) (string, error) {
	p, err := c.kube.CoreV1().Pods(Namespace(t.ApplicationID)).Get(ctx, "actions-"+id, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return "missing", nil
	}
	if err != nil {
		return "", err
	}
	if err = owned(p, t); err != nil {
		return "", err
	}
	if p.DeletionTimestamp != nil {
		return "deleting", nil
	}
	return string(p.Status.Phase), nil
}
func (c *Client) DeleteActionsPod(ctx context.Context, t Target, id string) (bool, error) {
	if err := beforeStep(ctx, t); err != nil {
		return false, err
	}
	api := c.kube.CoreV1().Pods(Namespace(t.ApplicationID))
	p, err := api.Get(ctx, "actions-"+id, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	if err = owned(p, t); err != nil {
		return false, err
	}
	if p.DeletionTimestamp == nil {
		err = api.Delete(ctx, p.Name, deleteOptions(p))
		if apierrors.IsNotFound(err) {
			return true, nil
		}
	}
	return false, err
}
func (c *Client) DeleteActionsConfig(ctx context.Context, t Target, id string) error {
	if err := beforeStep(ctx, t); err != nil {
		return err
	}
	api := c.kube.CoreV1().Secrets(Namespace(t.ApplicationID))
	s, err := api.Get(ctx, "actions-"+id, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if err = owned(s, t); err != nil {
		return err
	}
	err = api.Delete(ctx, s.Name, deleteOptions(s))
	if apierrors.IsNotFound(err) {
		return nil
	}
	return err
}

func (c *Client) waitActions(ctx context.Context, t Target) error {
	if c.actionsReconcile == nil {
		return fmt.Errorf("Managed Actions controller is unavailable")
	}
	for {
		if err := beforeStep(ctx, t); err != nil {
			return err
		}
		if err := c.actionsReconcile(ctx, t); err != nil {
			return err
		}
		ready := true
		for name, s := range t.Spec.Services {
			if s.Actions == nil {
				continue
			}
			status, err := c.actionsObserve(ctx, t, name, s)
			if err != nil {
				return err
			}
			ready = ready && (status.Status == "ready" || status.Status == "stopped")
		}
		if ready {
			return nil
		}
		if err := sleepContext(ctx, 2*time.Second); err != nil {
			return err
		}
	}
}

func mergeActionsSelector(s map[string]string) map[string]string {
	if s == nil {
		s = map[string]string{}
	}
	s["hakopod.io/actions-runtime"] = "ready"
	return s
}
func (c *Client) HasActionsConfig(ctx context.Context, t Target, id string) (bool, error) {
	secret, err := c.kube.CoreV1().Secrets(Namespace(t.ApplicationID)).Get(ctx, "actions-"+id, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if err = owned(secret, t); err != nil {
		return false, err
	}
	return len(secret.Data["config"]) > 0, nil
}
