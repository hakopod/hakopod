package cluster

import (
	"context"
	_ "embed"
	"errors"
	"reflect"
	"time"

	"github.com/hakopod/hakopod/internal/actions"
	"github.com/hakopod/hakopod/internal/spec"
	corev1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

//go:embed actions-bitbucket-daemon.sh
var bitbucketActionsDaemon string

func bitbucketActionsResources(s spec.Service) (corev1.ResourceRequirements, corev1.ResourceRequirements) {
	profile := spec.EffectiveResources(s)
	manager := corev1.ResourceRequirements{
		Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("250m"), corev1.ResourceMemory: resource.MustParse("512Mi"), corev1.ResourceEphemeralStorage: resource.MustParse("256Mi")},
		Limits:   corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("500m"), corev1.ResourceMemory: resource.MustParse("1Gi"), corev1.ResourceEphemeralStorage: resource.MustParse("2Gi")},
	}
	daemon := corev1.ResourceRequirements{Requests: corev1.ResourceList{}, Limits: corev1.ResourceList{}}
	for _, allocation := range []struct {
		resources corev1.ResourceList
		manager   corev1.ResourceList
		cpu, mem  string
	}{{daemon.Requests, manager.Requests, profile.CPURequest, profile.MemoryRequest}, {daemon.Limits, manager.Limits, profile.CPULimit, profile.MemoryLimit}} {
		cpu, memory := resource.MustParse(allocation.cpu), resource.MustParse(allocation.mem)
		cpu.Sub(resource.MustParse("100m"))
		cpu.Sub(allocation.manager[corev1.ResourceCPU])
		memory.Sub(resource.MustParse("512Mi"))
		memory.Sub(allocation.manager[corev1.ResourceMemory])
		allocation.resources[corev1.ResourceCPU], allocation.resources[corev1.ResourceMemory] = cpu, memory
	}
	workspace := resource.NewQuantity(spec.ActionsWorkspaceGiB(s.Actions)<<30, resource.BinarySI)
	daemon.Requests[corev1.ResourceEphemeralStorage] = workspace.DeepCopy()
	workspace.Add(resource.MustParse("1Gi"))
	daemon.Limits[corev1.ResourceEphemeralStorage] = workspace.DeepCopy()
	return manager, daemon
}

// BitbucketActionsPod builds a repository-dedicated native runner candidate.
// It deliberately has no job-count flag, busy-state inference, or automatic
// restart with the same registration. Admission remains disabled until native
// startup, credential isolation and termination have passed real acceptance.
func BitbucketActionsPod(t Target, service, id string, s spec.Service, registration actions.BitbucketManagerConfig, runtime BitbucketActionsRuntime) (*corev1.Pod, error) {
	if err := validateBitbucketActions(t, service, id, s, registration, runtime); err != nil {
		return nil, err
	}
	pod := actionsPod(t, service, id, s)
	pod.ObjectMeta = bitbucketActionsMetadata(t, service, id, registration, runtime)
	// A pool timeout is not a step timeout for a reusable runner. Retirement
	// must first prevent new acquisition and resolve every assigned step.
	pod.Spec.ActiveDeadlineSeconds = nil
	pod.Spec.TerminationGracePeriodSeconds = ptr(int64(120))
	pod.Spec.ShareProcessNamespace = ptr(false)
	pod.Spec.OS = &corev1.PodOS{Name: corev1.Linux}
	pod.Spec.DNSPolicy = corev1.DNSClusterFirst
	pod.Spec.SchedulerName = corev1.DefaultSchedulerName
	managerResources, daemonResources := bitbucketActionsResources(s)
	// Native Bitbucket reads root-owned private daemon log files directly.
	// UID zero has no capabilities here and remains inside the gVisor sandbox;
	// it has no host namespaces, node paths or manager-visible cluster token.
	managerSecurity := &corev1.SecurityContext{RunAsUser: ptr(int64(0)), RunAsGroup: ptr(int64(1001)), RunAsNonRoot: ptr(false), ReadOnlyRootFilesystem: ptr(true), AllowPrivilegeEscalation: ptr(false), Capabilities: &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}}}
	prepare := corev1.Container{
		Name: "prepare", Image: ActionsDaemonImage, ImagePullPolicy: corev1.PullIfNotPresent,
		Command:   []string{"sh", "-ec", "umask 077; mkdir -p /var/lib/docker/containers /run/hakopod-manager/tmp"},
		Resources: managerResources, SecurityContext: managerSecurity.DeepCopy(),
		VolumeMounts: []corev1.VolumeMount{{Name: "docker-data", MountPath: "/var/lib/docker"}, {Name: "manager-state", MountPath: "/run/hakopod-manager"}},
	}
	daemon := pod.Spec.InitContainers[1]
	daemon.Command = []string{"sh", "-ec", bitbucketActionsDaemon}
	daemon.ImagePullPolicy = corev1.PullIfNotPresent
	daemon.Resources = daemonResources
	daemon.SecurityContext.AllowPrivilegeEscalation = ptr(false)
	daemon.StartupProbe.SuccessThreshold = 1
	daemon.VolumeMounts = []corev1.VolumeMount{{Name: "workspace", MountPath: "/tmp"}, {Name: "docker-data", MountPath: "/var/lib/docker"}, {Name: "docker-socket", MountPath: "/var/run"}}
	secretEnv := func(name, key string) corev1.EnvVar {
		return corev1.EnvVar{Name: name, ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: pod.Name}, Key: key}}}
	}
	manager := corev1.Container{
		Name: service, Image: runtime.ManagerImage, ImagePullPolicy: corev1.PullIfNotPresent,
		Resources: managerResources, SecurityContext: managerSecurity.DeepCopy(),
		Env: []corev1.EnvVar{
			{Name: "ACCOUNT_UUID", Value: registration.Target.Workspace}, {Name: "REPOSITORY_UUID", Value: registration.Target.Repository}, {Name: "RUNNER_UUID", Value: registration.RunnerID},
			secretEnv("OAUTH_CLIENT_ID", "oauth_client_id"), secretEnv("OAUTH_CLIENT_SECRET", "oauth_client_secret"),
			{Name: "WORKING_DIRECTORY", Value: "/tmp"}, {Name: "HOME", Value: "/run/hakopod-manager"},
		},
		VolumeMounts: []corev1.VolumeMount{
			{Name: "workspace", MountPath: "/tmp"}, {Name: "docker-data", MountPath: "/var/lib/docker/containers", SubPath: "containers", ReadOnly: true},
			{Name: "docker-socket", MountPath: "/var/run"}, {Name: "manager-state", MountPath: "/run/hakopod-manager"},
		},
	}
	pod.Spec.InitContainers, pod.Spec.Containers = []corev1.Container{prepare, daemon}, []corev1.Container{manager}
	for _, containers := range [][]corev1.Container{pod.Spec.InitContainers, pod.Spec.Containers} {
		for i := range containers {
			containers[i].TerminationMessagePath = "/dev/termination-log"
			containers[i].TerminationMessagePolicy = corev1.TerminationMessageReadFile
		}
	}
	workspaceBytes := spec.ActionsWorkspaceGiB(s.Actions) << 30
	// Both the shared work and daemon data count against the same declared
	// workspace allowance. Reserve 16 MiB within it for the private socket.
	workBytes := workspaceBytes / 2
	pod.Spec.Volumes = []corev1.Volume{
		{Name: "workspace", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{SizeLimit: resource.NewQuantity(workBytes, resource.BinarySI)}}},
		{Name: "docker-data", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{SizeLimit: resource.NewQuantity(workspaceBytes-workBytes-(16<<20), resource.BinarySI)}}},
		{Name: "docker-socket", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{SizeLimit: ptr(resource.MustParse("16Mi"))}}},
		{Name: "manager-state", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{Medium: corev1.StorageMediumMemory, SizeLimit: ptr(resource.MustParse("64Mi"))}}},
	}
	return pod, nil
}

func bitbucketActionsObjectMatches(current, wanted metav1.Object, t Target) bool {
	return owned(current, t) == nil && current.GetNamespace() == wanted.GetNamespace() && current.GetName() == wanted.GetName() && current.GetDeletionTimestamp() == nil &&
		current.GetLabels()[serviceKey] == wanted.GetLabels()[serviceKey] && current.GetLabels()[bitbucketActionsProviderLabel] == "bitbucket" && current.GetLabels()[bitbucketActionsSlotLabel] == wanted.GetLabels()[bitbucketActionsSlotLabel] &&
		current.GetAnnotations()[bitbucketActionsBindingAnnotation] == wanted.GetAnnotations()[bitbucketActionsBindingAnnotation] && current.GetAnnotations()["hakopod.io/actions-runner-id"] == wanted.GetAnnotations()["hakopod.io/actions-runner-id"]
}

func (c *Client) bitbucketActionsNamespace(ctx context.Context, t Target) error {
	namespace, err := c.kube.CoreV1().Namespaces().Get(ctx, Namespace(t.ApplicationID), metav1.GetOptions{})
	if err != nil || owned(namespace, t) != nil || namespace.Labels["hakopod.io/workload-kind"] != "actions" || namespace.DeletionTimestamp != nil {
		return errors.New("Bitbucket requires the existing owned Actions application namespace")
	}
	return nil
}

func (c *Client) SaveBitbucketActionsConfig(ctx context.Context, t Target, service, id string, s spec.Service, registration actions.BitbucketManagerConfig, runtime BitbucketActionsRuntime) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	secret, err := bitbucketActionsSecret(t, service, id, s, registration, runtime)
	if err != nil {
		return err
	}
	if err = c.bitbucketActionsNamespace(ctx, t); err != nil {
		return err
	}
	if err = beforeStep(ctx, t); err != nil {
		return err
	}
	_, err = c.kube.CoreV1().Secrets(secret.Namespace).Create(ctx, secret, metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		current, readErr := c.kube.CoreV1().Secrets(secret.Namespace).Get(ctx, secret.Name, metav1.GetOptions{})
		if readErr == nil && bitbucketActionsObjectMatches(current, secret, t) && current.Immutable != nil && *current.Immutable && current.Type == corev1.SecretTypeOpaque && reflect.DeepEqual(current.Data, secret.Data) {
			return nil
		}
		return errors.New("Bitbucket registration secret conflicts with its immutable runner slot")
	}
	if err != nil {
		return errors.New("Bitbucket registration secret could not be saved")
	}
	return nil
}

func (c *Client) StartBitbucketActionsPod(ctx context.Context, t Target, service, id string, s spec.Service, registration actions.BitbucketManagerConfig, runtime BitbucketActionsRuntime) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	pod, err := BitbucketActionsPod(t, service, id, s, registration, runtime)
	if err != nil {
		return err
	}
	secret, err := bitbucketActionsSecret(t, service, id, s, registration, runtime)
	if err != nil {
		return err
	}
	placement, err := c.actionsPoolPlacement(ctx, t, s)
	if err != nil {
		return err
	}
	if err = c.bitbucketActionsNamespace(ctx, t); err != nil {
		return err
	}
	runtimeClass, err := c.kube.NodeV1().RuntimeClasses().Get(ctx, ActionsRuntime, metav1.GetOptions{})
	if err != nil || (runtimeClass.Overhead != nil && !gitlabActionsOverheadFits(runtimeClass.Overhead.PodFixed)) {
		return errors.New("Bitbucket sandbox overhead exceeds its reserved runner budget")
	}
	currentSecret, err := c.kube.CoreV1().Secrets(secret.Namespace).Get(ctx, secret.Name, metav1.GetOptions{})
	if err != nil || !bitbucketActionsObjectMatches(currentSecret, secret, t) || currentSecret.Immutable == nil || !*currentSecret.Immutable || currentSecret.Type != corev1.SecretTypeOpaque || !reflect.DeepEqual(currentSecret.Data, secret.Data) {
		return errors.New("Bitbucket cannot verify its original immutable registration secret")
	}
	policyTarget := t
	policyTarget.policy = placement.policy
	for _, wanted := range policies(policyTarget) {
		if wanted.Name != "hakopod-default-deny" && wanted.Labels[serviceKey] != service {
			continue
		}
		current, readErr := c.kube.NetworkingV1().NetworkPolicies(wanted.Namespace).Get(ctx, wanted.Name, metav1.GetOptions{})
		if readErr != nil || owned(current, t) != nil || current.DeletionTimestamp != nil || !apiequality.Semantic.DeepEqual(current.Spec, wanted.Spec) {
			return errors.New("Bitbucket requires the existing Actions isolation and network policies")
		}
	}
	if placement.policy != nil {
		copy := *placement.policy
		copy.RuntimeClass, copy.MemoryRequest = ActionsRuntime, ""
		applyWorkloadPolicy(&copy, &pod.Spec)
		pinActionsNode(&pod.Spec, placement.nodeName)
	}
	pod.Spec.NodeSelector = placement.selector
	if err = beforeStep(ctx, t); err != nil {
		return err
	}
	_, err = c.kube.CoreV1().Pods(pod.Namespace).Create(ctx, pod, metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		current, readErr := c.kube.CoreV1().Pods(pod.Namespace).Get(ctx, pod.Name, metav1.GetOptions{})
		if readErr == nil && bitbucketActionsObjectMatches(current, pod, t) && gitlabActionsPodMatches(current, pod) {
			return nil
		}
		return errors.New("Bitbucket runner pod conflicts with its recorded runtime and slot")
	}
	if err != nil {
		return errors.New("Bitbucket runner pod could not be created")
	}
	return nil
}
