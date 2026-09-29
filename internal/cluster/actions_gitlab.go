package cluster

import (
	"context"
	"errors"
	"fmt"
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

// gitlabActionsPod is an unqualified native execution candidate. Provider
// selection remains gated until real jobs prove this runtime's isolation and
// lifecycle. It reuses Actions' sandbox, daemon and placement resource envelope.
func gitlabActionsPod(t Target, service, id string, s spec.Service, registration actions.GitLabManagerConfig, runtime GitLabActionsRuntime) (*corev1.Pod, error) {
	if err := validateGitLabActions(t, service, id, s, registration, runtime); err != nil {
		return nil, err
	}
	pod := actionsPod(t, service, id, s)
	pod.ObjectMeta = gitlabActionsMetadata(t, service, id, registration, runtime)
	pod.Spec.ActiveDeadlineSeconds = ptr(registration.TimeoutSeconds + 180)
	pod.Spec.TerminationGracePeriodSeconds = ptr(int64(60))
	pod.Spec.OS = &corev1.PodOS{Name: corev1.Linux}
	pod.Spec.ShareProcessNamespace = ptr(false)
	pod.Spec.DNSPolicy = corev1.DNSClusterFirst
	pod.Spec.SchedulerName = corev1.DefaultSchedulerName
	safe := &corev1.SecurityContext{RunAsUser: ptr(int64(1001)), RunAsGroup: ptr(int64(1001)), RunAsNonRoot: ptr(true), ReadOnlyRootFilesystem: ptr(true), AllowPrivilegeEscalation: ptr(false), Capabilities: &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}}}
	managerResources := actionsResources(s, 1)
	daemonResources := actionsResources(s, 3)
	// The entire disk-backed workspace belongs to the private daemon. Keep the
	// same summed storage reservation used by the shared Actions namespace quota.
	for _, resources := range []struct{ manager, daemon corev1.ResourceList }{{managerResources.Requests, daemonResources.Requests}, {managerResources.Limits, daemonResources.Limits}} {
		resources.manager[corev1.ResourceEphemeralStorage], resources.daemon[corev1.ResourceEphemeralStorage] = resources.daemon[corev1.ResourceEphemeralStorage], resources.manager[corev1.ResourceEphemeralStorage]
	}
	prepare := corev1.Container{
		Name: "prepare", Image: runtime.Images.Manager, ImagePullPolicy: corev1.PullIfNotPresent,
		Command:         []string{"sh", "-ec", "umask 077; cp /run/hakopod-input/config.toml /run/hakopod-manager/config.toml; chmod 0600 /run/hakopod-manager/config.toml"},
		SecurityContext: safe.DeepCopy(), Resources: managerResources,
		VolumeMounts: []corev1.VolumeMount{{Name: "manager-state", MountPath: gitlabActionsManagerDirectory}, {Name: "registration", MountPath: "/run/hakopod-input", ReadOnly: true}},
	}
	daemon := pod.Spec.InitContainers[1]
	daemon.Resources = daemonResources
	daemon.ImagePullPolicy = corev1.PullIfNotPresent
	daemon.StartupProbe.SuccessThreshold = 1
	daemon.VolumeMounts = []corev1.VolumeMount{{Name: "runner", MountPath: "/home/runner"}, {Name: "transport-policy", MountPath: gitlabActionsDaemonPolicyDirectory, ReadOnly: true}}
	manager := corev1.Container{
		Name: service, Image: runtime.Images.Manager, ImagePullPolicy: corev1.PullIfNotPresent, WorkingDir: gitlabActionsManagerDirectory,
		Command:         []string{"/usr/bin/gitlab-runner", "--log-level", "warning", "run-single", "-c", gitlabActionsManagerDirectory + "/config.toml", "-r", registration.Name, "--max-builds", "1", "--wait-timeout", "120"},
		SecurityContext: safe.DeepCopy(), Resources: managerResources,
		Env:          []corev1.EnvVar{{Name: "HOME", Value: gitlabActionsManagerDirectory}},
		VolumeMounts: []corev1.VolumeMount{{Name: "manager-state", MountPath: gitlabActionsManagerDirectory}, {Name: "manager-tmp", MountPath: "/tmp"}, {Name: "transport-policy", MountPath: gitlabActionsPolicyDirectory, ReadOnly: true}},
		Lifecycle:    &corev1.Lifecycle{PreStop: &corev1.LifecycleHandler{Exec: &corev1.ExecAction{Command: []string{"sh", "-ec", "kill -QUIT 1; while kill -0 1 2>/dev/null; do sleep 1; done"}}}},
	}
	pod.Spec.InitContainers, pod.Spec.Containers = []corev1.Container{prepare, daemon}, []corev1.Container{manager}
	for _, containers := range [][]corev1.Container{pod.Spec.InitContainers, pod.Spec.Containers} {
		for i := range containers {
			containers[i].TerminationMessagePath = "/dev/termination-log"
			containers[i].TerminationMessagePolicy = corev1.TerminationMessageReadFile
		}
	}
	pod.Spec.Volumes = []corev1.Volume{
		{Name: "runner", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{SizeLimit: ptr(resource.MustParse(fmt.Sprintf("%dGi", spec.ActionsWorkspaceGiB(s.Actions))))}}},
		{Name: "manager-state", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{Medium: corev1.StorageMediumMemory, SizeLimit: ptr(resource.MustParse("2Mi"))}}},
		{Name: "manager-tmp", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{Medium: corev1.StorageMediumMemory, SizeLimit: ptr(resource.MustParse("64Mi"))}}},
		{Name: "registration", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: pod.Name, DefaultMode: ptr(int32(0440)), Items: []corev1.KeyToPath{{Key: "config.toml", Path: "config.toml"}}}}},
		{Name: "transport-policy", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: pod.Name}, DefaultMode: ptr(int32(0444))}}},
	}
	return pod, nil
}

func gitlabActionsObjectMatches(current, wanted metav1.Object, t Target) bool {
	return owned(current, t) == nil && current.GetNamespace() == wanted.GetNamespace() && current.GetName() == wanted.GetName() && current.GetDeletionTimestamp() == nil &&
		current.GetLabels()[serviceKey] == wanted.GetLabels()[serviceKey] && current.GetLabels()[gitlabActionsProviderLabel] == "gitlab" && current.GetLabels()[gitlabActionsSlotLabel] == wanted.GetLabels()[gitlabActionsSlotLabel] &&
		current.GetAnnotations()[gitlabActionsBindingAnnotation] == wanted.GetAnnotations()[gitlabActionsBindingAnnotation] && current.GetAnnotations()["hakopod.io/actions-runner-id"] == wanted.GetAnnotations()["hakopod.io/actions-runner-id"]
}

func (c *Client) gitlabActionsNamespace(ctx context.Context, t Target) error {
	namespace, err := c.kube.CoreV1().Namespaces().Get(ctx, Namespace(t.ApplicationID), metav1.GetOptions{})
	if err != nil || owned(namespace, t) != nil || namespace.Labels["hakopod.io/workload-kind"] != "actions" || namespace.DeletionTimestamp != nil {
		return errors.New("GitLab requires the existing owned Actions application namespace")
	}
	return nil
}

// SaveGitLabActionsConfig persists credentials and public policy separately.
// Every write is fenced; retries accept only exactly matching immutable content.
func (c *Client) SaveGitLabActionsConfig(ctx context.Context, t Target, service, id string, s spec.Service, registration actions.GitLabManagerConfig, runtime GitLabActionsRuntime) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	secret, public, err := gitlabActionsArtifacts(t, service, id, s, registration, runtime)
	if err != nil {
		return err
	}
	if err = c.gitlabActionsNamespace(ctx, t); err != nil {
		return err
	}
	if err = beforeStep(ctx, t); err != nil {
		return err
	}
	_, err = c.kube.CoreV1().Secrets(secret.Namespace).Create(ctx, secret, metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		current, readErr := c.kube.CoreV1().Secrets(secret.Namespace).Get(ctx, secret.Name, metav1.GetOptions{})
		if readErr != nil || !gitlabActionsObjectMatches(current, secret, t) || current.Immutable == nil || !*current.Immutable || current.Type != corev1.SecretTypeOpaque || !reflect.DeepEqual(current.Data, secret.Data) {
			return errors.New("GitLab registration secret conflicts with the recorded immutable slot")
		}
		err = nil
	}
	if err != nil {
		// Kubernetes errors may quote rejected secret request bodies. Never
		// propagate their diagnostics through controller logs or public status.
		return errors.New("GitLab registration secret could not be saved")
	}
	if err = beforeStep(ctx, t); err != nil {
		return err
	}
	_, err = c.kube.CoreV1().ConfigMaps(public.Namespace).Create(ctx, public, metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		current, readErr := c.kube.CoreV1().ConfigMaps(public.Namespace).Get(ctx, public.Name, metav1.GetOptions{})
		if readErr != nil || !gitlabActionsObjectMatches(current, public, t) || current.Immutable == nil || !*current.Immutable || !reflect.DeepEqual(current.Data, public.Data) || len(current.BinaryData) != 0 {
			return errors.New("GitLab transport policy conflicts with the recorded immutable slot")
		}
		err = nil
	}
	if err != nil {
		return errors.New("GitLab transport policy could not be saved")
	}
	return nil
}

func (c *Client) gitlabActionsArtifactsReady(ctx context.Context, t Target, secret *corev1.Secret, public *corev1.ConfigMap) error {
	currentSecret, err := c.kube.CoreV1().Secrets(secret.Namespace).Get(ctx, secret.Name, metav1.GetOptions{})
	if err != nil || !gitlabActionsObjectMatches(currentSecret, secret, t) || currentSecret.Immutable == nil || !*currentSecret.Immutable || currentSecret.Type != corev1.SecretTypeOpaque || !reflect.DeepEqual(currentSecret.Data, secret.Data) {
		return errors.New("GitLab cannot verify its original immutable registration secret")
	}
	currentPolicy, err := c.kube.CoreV1().ConfigMaps(public.Namespace).Get(ctx, public.Name, metav1.GetOptions{})
	if err != nil || !gitlabActionsObjectMatches(currentPolicy, public, t) || currentPolicy.Immutable == nil || !*currentPolicy.Immutable || !reflect.DeepEqual(currentPolicy.Data, public.Data) || len(currentPolicy.BinaryData) != 0 {
		return errors.New("GitLab cannot verify its immutable native transport policy")
	}
	return nil
}

func (c *Client) gitlabActionsPoliciesReady(ctx context.Context, t Target, service string) error {
	for _, wanted := range policies(t) {
		if wanted.Name != "hakopod-default-deny" && wanted.Labels[serviceKey] != service {
			continue
		}
		current, err := c.kube.NetworkingV1().NetworkPolicies(wanted.Namespace).Get(ctx, wanted.Name, metav1.GetOptions{})
		if err != nil || owned(current, t) != nil || current.DeletionTimestamp != nil || !apiequality.Semantic.DeepEqual(current.Spec, wanted.Spec) {
			return errors.New("GitLab requires the existing Actions isolation and service network policies")
		}
	}
	return nil
}

// StartGitLabActionsPod verifies decrypted original registration material before
// creating a pod. This does not enable GitLab provider selection in the API.
func (c *Client) StartGitLabActionsPod(ctx context.Context, t Target, service, id string, s spec.Service, registration actions.GitLabManagerConfig, runtime GitLabActionsRuntime) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	secret, public, err := gitlabActionsArtifacts(t, service, id, s, registration, runtime)
	if err != nil {
		return err
	}
	placement, err := c.actionsPoolPlacement(ctx, t, s)
	if err != nil {
		return err
	}
	if err = c.gitlabActionsNamespace(ctx, t); err != nil {
		return err
	}
	runtimeClass, err := c.kube.NodeV1().RuntimeClasses().Get(ctx, ActionsRuntime, metav1.GetOptions{})
	if err != nil {
		return errors.New("GitLab sandbox overhead could not be verified")
	}
	if runtimeClass.Overhead != nil && !gitlabActionsOverheadFits(runtimeClass.Overhead.PodFixed) {
		return errors.New("GitLab sandbox overhead exceeds the reserved slot budget")
	}
	if err = c.gitlabActionsArtifactsReady(ctx, t, secret, public); err != nil {
		return err
	}
	policyTarget := t
	policyTarget.policy = placement.policy
	if err = c.gitlabActionsPoliciesReady(ctx, policyTarget, service); err != nil {
		return err
	}
	pod, err := gitlabActionsPod(t, service, id, s, registration, runtime)
	if err != nil {
		return err
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
		if readErr != nil || !gitlabActionsObjectMatches(current, pod, t) || !gitlabActionsPodMatches(current, pod) {
			return errors.New("GitLab pod conflicts with the recorded runtime and slot identity")
		}
		return nil
	}
	if err != nil {
		return errors.New("GitLab runner pod could not be created")
	}
	return nil
}

func gitlabActionsPodMatches(current, wanted *corev1.Pod) bool {
	a, b := current.Spec.DeepCopy(), wanted.Spec.DeepCopy()
	// The scheduler and standard admission controllers populate only these
	// non-workload fields. Everything executable, mounted or privileged must
	// match the generated pod exactly on an idempotent start.
	if a.NodeName != "" && b.NodeName == "" {
		a.NodeName = ""
	}
	if a.ServiceAccountName == "default" {
		a.ServiceAccountName = ""
	}
	if a.DeprecatedServiceAccount == "default" {
		a.DeprecatedServiceAccount = ""
	}
	if a.Priority != nil && *a.Priority == 0 {
		a.Priority = nil
	}
	if a.PreemptionPolicy != nil && *a.PreemptionPolicy == corev1.PreemptLowerPriority {
		a.PreemptionPolicy = nil
	}
	if len(a.Overhead) > 0 {
		if !gitlabActionsOverheadFits(a.Overhead) {
			return false
		}
		a.Overhead = nil
	}
	tolerations := a.Tolerations[:0]
	for _, toleration := range a.Tolerations {
		if (toleration.Key == "node.kubernetes.io/not-ready" || toleration.Key == "node.kubernetes.io/unreachable") && toleration.Operator == corev1.TolerationOpExists && toleration.Effect == corev1.TaintEffectNoExecute && toleration.Value == "" && toleration.TolerationSeconds != nil && *toleration.TolerationSeconds == 300 {
			continue
		}
		tolerations = append(tolerations, toleration)
	}
	a.Tolerations = tolerations
	if len(a.Tolerations) == 0 {
		a.Tolerations = nil
	}
	return apiequality.Semantic.DeepEqual(a, b)
}

func gitlabActionsOverheadFits(overhead corev1.ResourceList) bool {
	for name, amount := range overhead {
		if amount.Sign() < 0 || (name != corev1.ResourceCPU && name != corev1.ResourceMemory) {
			return false
		}
	}
	cpu, memory := overhead[corev1.ResourceCPU], overhead[corev1.ResourceMemory]
	return cpu.Cmp(resource.MustParse("100m")) <= 0 && memory.Cmp(resource.MustParse("512Mi")) <= 0
}

// DeleteGitLabActionsPolicy follows pod deletion, including termination. It
// never removes the public policy while a manager or native helper may use it.
func (c *Client) DeleteGitLabActionsPolicy(ctx context.Context, t Target, id string) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if !gitlabActionsSlotID.MatchString(id) {
		return errors.New("GitLab runner slot identity is invalid")
	}
	if err := beforeStep(ctx, t); err != nil {
		return err
	}
	name, namespace := "actions-"+id, Namespace(t.ApplicationID)
	if pod, err := c.kube.CoreV1().Pods(namespace).Get(ctx, name, metav1.GetOptions{}); err == nil {
		if err = owned(pod, t); err != nil {
			return err
		}
		return errors.New("GitLab transport policy must remain until its runner pod is gone")
	} else if !apierrors.IsNotFound(err) {
		return errors.New("GitLab runner absence could not be verified")
	}
	api := c.kube.CoreV1().ConfigMaps(namespace)
	current, err := api.Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return errors.New("GitLab transport policy could not be inspected")
	}
	if owned(current, t) != nil || current.Labels[gitlabActionsProviderLabel] != "gitlab" || current.Labels[gitlabActionsSlotLabel] != id {
		return errors.New("GitLab transport policy belongs to a different owner or slot")
	}
	if err = beforeStep(ctx, t); err != nil {
		return err
	}
	if err = api.Delete(ctx, name, deleteOptions(current)); err != nil && !apierrors.IsNotFound(err) {
		return errors.New("GitLab transport policy could not be removed")
	}
	if _, err = api.Get(ctx, name, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		return errors.New("GitLab transport policy absence has not been confirmed")
	}
	return nil
}

// DeleteGitLabActionsConfig waits for observed pod and Secret absence. An
// accepted delete may leave finalizers or an ownership replacement behind.
func (c *Client) DeleteGitLabActionsConfig(ctx context.Context, t Target, id string) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if !gitlabActionsSlotID.MatchString(id) {
		return errors.New("GitLab runner slot identity is invalid")
	}
	if err := beforeStep(ctx, t); err != nil {
		return err
	}
	name, namespace := "actions-"+id, Namespace(t.ApplicationID)
	if _, err := c.kube.CoreV1().Pods(namespace).Get(ctx, name, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		return errors.New("GitLab registration must remain until its runner pod absence is confirmed")
	}
	api := c.kube.CoreV1().Secrets(namespace)
	current, err := api.Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return errors.New("GitLab registration could not be inspected")
	}
	if owned(current, t) != nil || current.Labels[gitlabActionsProviderLabel] != "gitlab" || current.Labels[gitlabActionsSlotLabel] != id {
		return errors.New("GitLab registration belongs to a different owner or slot")
	}
	if err = beforeStep(ctx, t); err != nil {
		return err
	}
	if err = api.Delete(ctx, name, deleteOptions(current)); err != nil && !apierrors.IsNotFound(err) {
		return errors.New("GitLab registration could not be removed")
	}
	if _, err = api.Get(ctx, name, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		return errors.New("GitLab registration absence has not been confirmed")
	}
	return nil
}
