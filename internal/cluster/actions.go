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
	if err := c.actionsRuntimeAvailable(ctx); err != nil {
		return err
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

func (c *Client) actionsRuntimeAvailable(ctx context.Context) error {
	r, err := c.kube.NodeV1().RuntimeClasses().Get(ctx, ActionsRuntime, metav1.GetOptions{})
	if err != nil || r.Handler != ActionsRuntime || r.Labels[managedBy] != "hakopod" || r.Scheduling == nil || r.Scheduling.NodeSelector["hakopod.io/actions-runtime"] != "ready" {
		return fmt.Errorf("Managed Actions sandbox is not installed; enable it in the installation's runtime setup")
	}
	return nil
}

type actionsPlacement struct {
	nodeName         string
	policy           *WorkloadPolicy
	selector         map[string]string
	workspaceProfile ActionsWorkspaceProfile
}

// ActionsScopeAvailable checks discovery readiness without inventing a pool or
// applying an ordinary workload's allocation to an Actions request.
func (c *Client) ActionsScopeAvailable(ctx context.Context, t Target) error {
	if err := c.actionsRuntimeAvailable(ctx); err != nil {
		return err
	}
	policy, err := c.placementPolicy(ctx, t, "actions")
	if err != nil {
		return err
	}
	_, err = c.availableActionsPlacement(ctx, policy, spec.Service{})
	return err
}

// ActionsPoolAvailable checks the pool's effective placement before the
// controller requests a single-job registration from GitHub.
func (c *Client) ActionsPoolAvailable(ctx context.Context, t Target, s spec.Service) error {
	_, err := c.actionsPoolPlacement(ctx, t, s)
	return err
}

func (c *Client) actionsPoolPlacement(ctx context.Context, t Target, s spec.Service) (*actionsPlacement, error) {
	if err := c.actionsRuntimeAvailable(ctx); err != nil {
		return nil, err
	}
	policy, err := c.workloadPolicy(ctx, t)
	if err != nil {
		return nil, err
	}
	return c.availableActionsPlacement(ctx, policy, s)
}

func (c *Client) availableActionsPlacement(ctx context.Context, policy *WorkloadPolicy, s spec.Service) (*actionsPlacement, error) {
	if c.options.DeploymentMode == DeploymentManagedCloud && policy == nil && c.options.DedicatedPublicTCPNode == "" && c.options.OperatorNodeLimit == 0 {
		return nil, fmt.Errorf("Managed Actions compute is not allocated to this environment")
	}
	p := &actionsPlacement{nodeName: s.NodeName, policy: policy, selector: mergeActionsSelector(nil), workspaceProfile: ActionsWorkspaceVFS}
	if s.Architecture != "" {
		p.selector["kubernetes.io/arch"] = s.Architecture
	}
	if policy != nil {
		if p.nodeName != "" && p.nodeName != policy.NodeName {
			return nil, fmt.Errorf("selected Managed Actions node conflicts with the node allocated by the runtime")
		}
		p.nodeName = policy.NodeName
		if policy.Pool != "" {
			p.selector["hakopod.com/pool"] = policy.Pool
		}
		if githubActionsService(s) {
			profile, err := normalizedActionsWorkspaceProfile(policy.ActionsWorkspaceProfile)
			if err != nil {
				return nil, err
			}
			p.workspaceProfile = profile
			if profile == ActionsWorkspaceSharedOverlay2V1 {
				p.selector[ActionsWorkspaceCapabilityLabel] = string(profile)
			}
		}
	}
	// A self-hosted operator opts in by enrolling a node after its functional
	// probe. Cloud allocation and explicit VFS policies never borrow this opt-in.
	selfHosted := c.options.DeploymentMode == "" || c.options.DeploymentMode == DeploymentSelfHosted
	preferShared := selfHosted && policy == nil && githubActionsService(s)
	if preferShared && p.nodeName == "" {
		// Query qualified nodes directly so an unrelated first inventory page
		// cannot hide enrollment in a larger installation.
		p.workspaceProfile = ActionsWorkspaceSharedOverlay2V1
		p.selector[ActionsWorkspaceCapabilityLabel] = string(ActionsWorkspaceSharedOverlay2V1)
		qualified, err := c.actionsPlacementNodes(ctx, p)
		if err != nil {
			return nil, err
		}
		for _, node := range qualified {
			if p.matches(node) {
				return p, nil
			}
		}
		p.workspaceProfile = ActionsWorkspaceVFS
		delete(p.selector, ActionsWorkspaceCapabilityLabel)
	}
	candidates, err := c.actionsPlacementNodes(ctx, p)
	if err != nil {
		return nil, err
	}
	if preferShared {
		for _, node := range candidates {
			if p.matches(node) && node.Labels[ActionsWorkspaceCapabilityLabel] == string(ActionsWorkspaceSharedOverlay2V1) {
				p.workspaceProfile = ActionsWorkspaceSharedOverlay2V1
				p.selector[ActionsWorkspaceCapabilityLabel] = string(ActionsWorkspaceSharedOverlay2V1)
				return p, nil
			}
		}
	}
	for _, node := range candidates {
		if p.matches(node) {
			return p, nil
		}
	}
	return nil, fmt.Errorf("No ready Managed Actions node matches this pool's placement")
}

func (c *Client) actionsPlacementNodes(ctx context.Context, p *actionsPlacement) ([]corev1.Node, error) {
	selector := labels.SelectorFromSet(p.selector)
	var candidates []corev1.Node
	if p.nodeName != "" {
		node, err := c.kube.CoreV1().Nodes().Get(ctx, p.nodeName, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return nil, fmt.Errorf("No ready Managed Actions node matches this pool's placement")
		}
		if err != nil {
			return nil, fmt.Errorf("Managed Actions placement could not be checked")
		}
		candidates = []corev1.Node{*node}
	} else {
		nodes, err := c.kube.CoreV1().Nodes().List(ctx, metav1.ListOptions{LabelSelector: selector.String(), Limit: 500})
		if err != nil {
			return nil, fmt.Errorf("Managed Actions placement could not be checked")
		}
		candidates = nodes.Items
	}
	return candidates, nil
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
	storageRequest, storageLimit := resource.MustParse("256Mi"), resource.MustParse("2Gi")
	if numerator != 3 {
		// Reserve the shared workspace and Docker data before scheduling a
		// runner. Leave another GiB for logs and its writable container layer.
		storageRequest = resource.MustParse(fmt.Sprintf("%dGi", spec.ActionsWorkspaceGiB(s.Actions)))
		storageLimit = resource.MustParse(fmt.Sprintf("%dGi", spec.ActionsWorkspaceGiB(s.Actions)+1))
	}
	return corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: part(p.CPURequest, true), corev1.ResourceMemory: part(p.MemoryRequest, false), corev1.ResourceEphemeralStorage: storageRequest}, Limits: corev1.ResourceList{corev1.ResourceCPU: part(p.CPULimit, true), corev1.ResourceMemory: part(p.MemoryLimit, false), corev1.ResourceEphemeralStorage: storageLimit}}
}

func actionsPod(t Target, service, id string, s spec.Service) *corev1.Pod {
	name := "actions-" + id
	safe := &corev1.SecurityContext{RunAsUser: ptr(int64(1001)), RunAsGroup: ptr(int64(1001)), RunAsNonRoot: ptr(true), AllowPrivilegeEscalation: ptr(false), Capabilities: &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}}}
	mounts := []corev1.VolumeMount{{Name: "runner", MountPath: "/home/runner"}}
	copyResources := actionsResources(s, 4)
	p := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: Namespace(t.ApplicationID), Labels: labelsFor(t, service)}, Spec: corev1.PodSpec{
		RuntimeClassName: ptr(ActionsRuntime), AutomountServiceAccountToken: ptr(false), EnableServiceLinks: ptr(false), RestartPolicy: corev1.RestartPolicyNever, ActiveDeadlineSeconds: ptr(s.Actions.TimeoutMinutes * 60), TerminationGracePeriodSeconds: ptr(int64(30)),
		SecurityContext: &corev1.PodSecurityContext{FSGroup: ptr(int64(1001)), SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault}},
		InitContainers: []corev1.Container{
			// The volume root belongs to kubelet. Copy files without trying to
			// restore its timestamps or ownership as the unprivileged runner.
			{Name: "prepare", Image: spec.ActionsRunnerImage, Command: []string{"sh", "-c", "cp -R /home/runner/. /runner/"}, SecurityContext: safe, Resources: copyResources, VolumeMounts: []corev1.VolumeMount{{Name: "runner", MountPath: "/runner"}}},
			{Name: "docker", Image: ActionsDaemonImage, Command: []string{"sh", "-c", actionsDaemon}, RestartPolicy: ptr(corev1.ContainerRestartPolicyAlways), Resources: actionsResources(s, 3),
				SecurityContext: &corev1.SecurityContext{Privileged: ptr(false), RunAsUser: ptr(int64(0)), Capabilities: &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}, Add: []corev1.Capability{"AUDIT_WRITE", "CHOWN", "DAC_OVERRIDE", "FOWNER", "FSETID", "KILL", "MKNOD", "NET_BIND_SERVICE", "NET_ADMIN", "NET_RAW", "SETFCAP", "SETGID", "SETPCAP", "SETUID", "SYS_ADMIN", "SYS_CHROOT", "SYS_PTRACE"}}},
				Env:             []corev1.EnvVar{{Name: "DOCKER_HOST", Value: "unix:///var/run/docker.sock"}},
				StartupProbe:    &corev1.Probe{ProbeHandler: corev1.ProbeHandler{Exec: &corev1.ExecAction{Command: []string{"docker", "info"}}}, PeriodSeconds: 2, FailureThreshold: 60, TimeoutSeconds: 2},
				VolumeMounts:    mounts},
		},
		Containers: []corev1.Container{{Name: service, Image: spec.ActionsRunnerImage, WorkingDir: "/home/runner", Command: []string{"sh", "-c", "exec /usr/bin/python3 /usr/local/lib/hakopod/observe.py"}, SecurityContext: safe, Resources: actionsResources(s, 1), Env: []corev1.EnvVar{{Name: "DOCKER_HOST", Value: "tcp://127.0.0.1:2375"}, {Name: "ACTIONS_RUNNER_PRINT_LOG_TO_STDOUT", Value: "1"}}, VolumeMounts: append(append([]corev1.VolumeMount{}, mounts...), corev1.VolumeMount{Name: "jit", MountPath: "/run/hakopod-jit", ReadOnly: true})}},
		Volumes:    []corev1.Volume{{Name: "runner", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{SizeLimit: ptr(resource.MustParse(fmt.Sprintf("%dGi", spec.ActionsWorkspaceGiB(s.Actions))))}}}, {Name: "jit", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: name, DefaultMode: ptr(int32(0440))}}}},
	}}
	if s.Architecture != "" {
		p.Spec.NodeSelector = map[string]string{"kubernetes.io/arch": s.Architecture}
	}
	if s.NodeName != "" {
		pinActionsNode(&p.Spec, s.NodeName)
	}
	return p
}

func pinActionsNode(pod *corev1.PodSpec, name string) {
	pod.Affinity = &corev1.Affinity{NodeAffinity: &corev1.NodeAffinity{RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{NodeSelectorTerms: []corev1.NodeSelectorTerm{{MatchFields: []corev1.NodeSelectorRequirement{{Key: "metadata.name", Operator: corev1.NodeSelectorOpIn, Values: []string{name}}}}}}}}
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
		return checkActionsConfig(current, t, service)
	}
	return err
}
func (c *Client) StartActionsPod(ctx context.Context, t Target, service, id string, s spec.Service) error {
	if err := beforeStep(ctx, t); err != nil {
		return err
	}
	// Running jobs keep their selected filesystem and Docker store. Capability
	// changes affect only a fresh pod, never a retry for an existing job.
	current, err := c.kube.CoreV1().Pods(Namespace(t.ApplicationID)).Get(ctx, "actions-"+id, metav1.GetOptions{})
	if err == nil {
		return c.existingActionsPod(ctx, t, service, current)
	}
	if !apierrors.IsNotFound(err) {
		return err
	}
	placement, err := c.actionsPoolPlacement(ctx, t, s)
	if err != nil {
		return err
	}
	p := actionsPod(t, service, id, s)
	if placement.policy != nil {
		copy := *placement.policy
		copy.RuntimeClass = ActionsRuntime
		copy.MemoryRequest = ""
		applyWorkloadPolicy(&copy, &p.Spec)
		// The trusted allocation names a Node object. Its hostname label
		// need not match; exact name affinity enforces the allocation.
		pinActionsNode(&p.Spec, placement.nodeName)
	}
	p.Spec.NodeSelector = placement.selector
	if err = applyActionsWorkspaceProfile(p, spec.ActionsWorkspaceGiB(s.Actions), placement.workspaceProfile); err != nil {
		return err
	}
	if err = beforeStep(ctx, t); err != nil {
		return err
	}
	secret, err := c.kube.CoreV1().Secrets(p.Namespace).Get(ctx, p.Name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	if err = checkActionsConfig(secret, t, service); err != nil {
		return err
	}
	if err = beforeStep(ctx, t); err != nil {
		return err
	}
	if err = c.revalidateActionsWorkspacePlacement(ctx, placement); err != nil {
		return err
	}
	_, err = c.kube.CoreV1().Pods(p.Namespace).Create(ctx, p, metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		current, e := c.kube.CoreV1().Pods(p.Namespace).Get(ctx, p.Name, metav1.GetOptions{})
		if e != nil {
			return e
		}
		return c.existingActionsPod(ctx, t, service, current)
	}
	return err
}

func (c *Client) existingActionsPod(ctx context.Context, t Target, service string, pod *corev1.Pod) error {
	if err := owned(pod, t); err != nil {
		return err
	}
	if pod.Labels[serviceKey] != service || pod.Labels["hakopod.io/actions-provider"] != "" && pod.Labels["hakopod.io/actions-provider"] != "github" {
		return fmt.Errorf("the Actions pod belongs to another service")
	}
	configurationBound := false
	for _, volume := range pod.Spec.Volumes {
		if volume.Name == "jit" && volume.Secret != nil && volume.Secret.SecretName == pod.Name {
			configurationBound = true
		}
	}
	if !configurationBound {
		return fmt.Errorf("the Actions pod does not use its original configuration")
	}
	secret, err := c.kube.CoreV1().Secrets(pod.Namespace).Get(ctx, pod.Name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	return checkActionsConfig(secret, t, service)
}

func checkActionsConfig(secret *corev1.Secret, t Target, service string) error {
	if err := owned(secret, t); err != nil {
		return err
	}
	if secret.Labels[serviceKey] != service {
		return fmt.Errorf("the Actions configuration belongs to another service")
	}
	if secret.Immutable == nil || !*secret.Immutable || len(secret.Data["config"]) == 0 || len(secret.Data["config"]) > 128<<10 {
		return fmt.Errorf("the original Actions configuration must be immutable and bounded")
	}
	return nil
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
