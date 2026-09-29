package cluster

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/actions"
	"github.com/hakopod/hakopod/internal/spec"
	"github.com/pelletier/go-toml/v2"
	corev1 "k8s.io/api/core/v1"
	nodev1 "k8s.io/api/node/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	clienttesting "k8s.io/client-go/testing"
)

const gitlabActionsFixtureSlot = "1234567890abcdef1234567890abcdef"
const gitlabActionsFixtureToken = "glrt-synthetic-private-manager-credential"

// These are explicitly synthetic development fixtures. Their image references,
// hashes and passed flag are not evidence of a published or qualified runtime.
func gitlabActionsDevelopmentFixture(t *testing.T) (Target, spec.Service, actions.GitLabManagerConfig, GitLabActionsRuntime) {
	t.Helper()
	target := runnerTarget(t)
	s := target.Spec.Services["runner"]
	s.Architecture = "amd64"
	s.Resources = &spec.Resources{CPURequest: "1", CPULimit: "2", MemoryRequest: "4Gi", MemoryLimit: "6Gi"}
	s.Actions = &spec.Actions{Provider: actions.ProviderGitLab, GitLab: &actions.GitLabTarget{URL: "https://gitlab.com", ProjectID: 12}, Credential: "gitlab-management", Labels: []string{"native"}, TimeoutMinutes: 15, WorkspaceSizeGiB: 8}
	target.Spec.Services["runner"] = s
	registration := actions.GitLabManagerConfig{SchemaVersion: 1, URL: "https://gitlab.com", RunnerID: "41", Name: "hakopod-" + gitlabActionsFixtureSlot, Token: gitlabActionsFixtureToken, TimeoutSeconds: 900}
	native := GitLabActionsRuntime{
		Images: GitLabActionsImages{
			Manager: "ghcr.io/hakopod/gitlab-runner@sha256:" + strings.Repeat("a", 64), Helper: "ghcr.io/hakopod/gitlab-runner-helper@sha256:" + strings.Repeat("b", 64), DefaultJobImage: "docker.io/library/debian@sha256:" + strings.Repeat("c", 64),
			Architecture: "amd64", Status: "passed", RunnerVersion: gitlabActionsRunnerVersion, SourceCommit: gitlabActionsSourceCommit,
			TransportSourceSHA256: strings.Repeat("d", 64), ManagerBinarySHA256: strings.Repeat("e", 64), HelperBinarySHA256: strings.Repeat("f", 64), VerificationReportSHA256: strings.Repeat("1", 64),
		},
		TransportPolicy: GitLabActionsTransportPolicy{SchemaVersion: 1, CoordinatorURL: "https://gitlab.com", ArtifactOrigins: []string{}},
	}
	native.Images.ExecutionReportSHA256 = strings.Repeat("2", 64)
	native.Images.Execution = &GitLabActionsExecutionQualification{Passed: true, Architecture: "amd64", Coordinators: []string{"https://gitlab.com"}, RunnerScopes: []string{"project"}, Checkout: true, Script: true, Artifacts: true, Services: true, JobIsolation: true, CredentialIsolation: true, Drain: true, Cleanup: true}
	s.Image = native.Images.Manager
	target.Spec.Services["runner"] = s
	return target, s, registration, native
}

func gitlabActionsDevelopmentClient(t *testing.T, target Target) (*Client, *fake.Clientset) {
	t.Helper()
	labels := labelsFor(target, "")
	labels["hakopod.io/workload-kind"] = "actions"
	objects := []k8sruntime.Object{
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: Namespace(target.ApplicationID), Labels: labels}},
		&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "development-node", Labels: map[string]string{"kubernetes.io/arch": "amd64", "hakopod.io/actions-runtime": "ready", "hakopod.com/pool": "private"}}, Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}}}},
		&nodev1.RuntimeClass{ObjectMeta: metav1.ObjectMeta{Name: ActionsRuntime, Labels: map[string]string{managedBy: "hakopod"}}, Handler: ActionsRuntime, Scheduling: &nodev1.Scheduling{NodeSelector: map[string]string{"hakopod.io/actions-runtime": "ready"}}, Overhead: &nodev1.Overhead{PodFixed: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("100m"), corev1.ResourceMemory: resource.MustParse("512Mi")}}},
	}
	for _, policy := range policies(target) {
		objects = append(objects, policy)
	}
	kube := fake.NewClientset(objects...)
	return &Client{kube: kube}, kube
}

func TestGitLabCandidateRuntimeSeparatesManagerCredentialsFromDockerAndJobs(t *testing.T) {
	target, s, registration, native := gitlabActionsDevelopmentFixture(t)
	pod, err := gitlabActionsPod(target, "runner", gitlabActionsFixtureSlot, s, registration, native)
	if err != nil {
		t.Fatal(err)
	}
	secret, policy, err := gitlabActionsArtifacts(target, "runner", gitlabActionsFixtureSlot, s, registration, native)
	if err != nil {
		t.Fatal(err)
	}
	if *pod.Spec.RuntimeClassName != ActionsRuntime || *pod.Spec.AutomountServiceAccountToken || *pod.Spec.ShareProcessNamespace || pod.Spec.HostNetwork || pod.Spec.HostPID || pod.Spec.HostIPC || pod.Spec.RestartPolicy != corev1.RestartPolicyNever || *pod.Spec.ActiveDeadlineSeconds != registration.TimeoutSeconds+180 || *pod.Spec.TerminationGracePeriodSeconds != 60 {
		t.Fatal("candidate lost its sandbox or bounded one-job lifetime")
	}
	for _, object := range []any{pod, policy} {
		data, _ := json.Marshal(object)
		if strings.Contains(string(data), registration.Token) || strings.Contains(string(data), s.Actions.Credential) {
			t.Fatal("private registration material entered a public Kubernetes artifact")
		}
	}
	if len(pod.Spec.InitContainers) != 2 || len(pod.Spec.Containers) != 1 || pod.Spec.InitContainers[1].Image != ActionsDaemonImage || pod.Spec.Containers[0].Image != native.Images.Manager || !strings.Contains(strings.Join(pod.Spec.Containers[0].Command, " "), "--max-builds 1 --wait-timeout 120") {
		t.Fatal("candidate did not select the explicit native manager and shared daemon")
	}
	volumes := map[string]corev1.Volume{}
	for _, volume := range pod.Spec.Volumes {
		volumes[volume.Name] = volume
		if volume.HostPath != nil || volume.Projected != nil || volume.PersistentVolumeClaim != nil {
			t.Fatal("candidate introduced node or persistent storage")
		}
	}
	if state := volumes["manager-state"].EmptyDir; state == nil || state.Medium != corev1.StorageMediumMemory || state.SizeLimit == nil || state.SizeLimit.Cmp(resource.MustParse("2Mi")) != 0 {
		t.Fatal("manager credential state is not bounded tmpfs")
	}
	for _, container := range append(pod.Spec.InitContainers, pod.Spec.Containers...) {
		if container.SecurityContext == nil || (container.SecurityContext.Privileged != nil && *container.SecurityContext.Privileged) {
			t.Fatal("candidate enabled outer privileged mode")
		}
		for _, mount := range container.VolumeMounts {
			if container.Name == "docker" && (mount.Name == "manager-state" || mount.Name == "registration" || mount.MountPath == gitlabActionsPolicyDirectory || mount.MountPath == gitlabActionsManagerDirectory) {
				t.Fatal("manager-private mounts entered Docker's filesystem")
			}
			if mount.Name == "registration" && (container.Name != "prepare" || !mount.ReadOnly) {
				t.Fatal("registration secret is mounted beyond the trusted preparation container")
			}
			if mount.Name == "transport-policy" && !mount.ReadOnly {
				t.Fatal("public transport policy is writable")
			}
		}
	}
	var config gitlabNativeConfig
	if toml.Unmarshal(secret.Data["config.toml"], &config) != nil || config.Concurrent != 1 || len(config.Runners) != 1 || config.Runners[0].Token != registration.Token || config.Runners[0].RequestConcurrency != 1 || config.Runners[0].Docker.HelperImage != native.Images.Helper {
		t.Fatal("native config is incomplete or lost the paired helper")
	}
	docker := config.Runners[0].Docker
	if docker.Privileged || docker.ServicesPrivileged || !docker.DisableCache || docker.ServicesLimit != 2 || docker.PidsLimit != 512 || docker.Host != "tcp://127.0.0.1:2375" || len(docker.Volumes) != 2 || docker.Volumes[1] != gitlabActionsDaemonPolicyDirectory+"/transport-policy.json:"+gitlabActionsPolicyDirectory+"/transport-policy.json:ro" {
		t.Fatal("native job isolation or daemon-visible policy binding is missing")
	}
	for _, binding := range docker.Volumes {
		if strings.Contains(binding, "manager") || strings.Contains(binding, "config.toml") || strings.Contains(binding, "hakopod-input") {
			t.Fatal("native helper or job receives a manager credential path")
		}
	}
	var decoded map[string]any
	if json.Unmarshal([]byte(policy.Data["transport-policy.json"]), &decoded) != nil || len(decoded) != 3 || decoded["coordinator_url"] != registration.URL || decoded["schema_version"] != float64(1) {
		t.Fatal("public policy does not match the native strict three-field schema")
	}
}

func TestGitLabCandidateRuntimePreservesTheDeclaredSlotBudget(t *testing.T) {
	target, s, registration, native := gitlabActionsDevelopmentFixture(t)
	pod, err := gitlabActionsPod(target, "runner", gitlabActionsFixtureSlot, s, registration, native)
	if err != nil {
		t.Fatal(err)
	}
	profile := spec.EffectiveResources(s)
	for _, value := range []struct {
		kind                  corev1.ResourceName
		request, limit, extra string
	}{{corev1.ResourceCPU, profile.CPURequest, profile.CPULimit, "100m"}, {corev1.ResourceMemory, profile.MemoryRequest, profile.MemoryLimit, "512Mi"}, {corev1.ResourceEphemeralStorage, "8448Mi", "11Gi", "0"}} {
		for _, limits := range []bool{false, true} {
			total := resource.MustParse(value.extra)
			bound := value.request
			if limits {
				bound = value.limit
			}
			for _, container := range []corev1.Container{pod.Spec.InitContainers[1], pod.Spec.Containers[0]} {
				resources := container.Resources.Requests
				if limits {
					resources = container.Resources.Limits
				}
				total.Add(resources[value.kind])
			}
			if total.Cmp(resource.MustParse(bound)) > 0 {
				t.Fatal("native manager/daemon exceeds its reserved slot budget", value.kind)
			}
		}
	}
	storage := pod.Spec.InitContainers[1].Resources.Requests[corev1.ResourceEphemeralStorage]
	if storage.Cmp(resource.MustParse("8Gi")) < 0 {
		t.Fatal("private Docker workspace is not reserved before scheduling")
	}
}

func TestGitLabCandidateRequiresActualPairedImageQualificationMetadata(t *testing.T) {
	for name, mutate := range map[string]func(*GitLabActionsRuntime){
		"binary-only result": func(v *GitLabActionsRuntime) { v.Images.Status = "binary_transport_passed" },
		"mutable manager":    func(v *GitLabActionsRuntime) { v.Images.Manager = "ghcr.io/hakopod/gitlab-runner:latest" },
		"missing helper":     func(v *GitLabActionsRuntime) { v.Images.Helper = "" },
		"upstream manager": func(v *GitLabActionsRuntime) {
			v.Images.Manager = "docker.io/gitlab/gitlab-runner@sha256:" + strings.Repeat("a", 64)
		},
		"upstream helper": func(v *GitLabActionsRuntime) {
			v.Images.Helper = "registry.gitlab.com/gitlab-org/gitlab-runner/gitlab-runner-helper@sha256:" + strings.Repeat("b", 64)
		},
		"different source":       func(v *GitLabActionsRuntime) { v.Images.SourceCommit = strings.Repeat("c", 40) },
		"missing source bundle":  func(v *GitLabActionsRuntime) { v.Images.TransportSourceSHA256 = "" },
		"missing image report":   func(v *GitLabActionsRuntime) { v.Images.VerificationReportSHA256 = "" },
		"same binary":            func(v *GitLabActionsRuntime) { v.Images.HelperBinarySHA256 = v.Images.ManagerBinarySHA256 },
		"mutable default image":  func(v *GitLabActionsRuntime) { v.Images.DefaultJobImage = "debian:latest" },
		"invalid policy version": func(v *GitLabActionsRuntime) { v.TransportPolicy.SchemaVersion = 2 },
		"noncanonical base":      func(v *GitLabActionsRuntime) { v.TransportPolicy.CoordinatorURL = "https://gitlab.com/" },
		"origin with path": func(v *GitLabActionsRuntime) {
			v.TransportPolicy.ArtifactOrigins = []string{"https://storage.example.com/path"}
		},
		"origin with credential": func(v *GitLabActionsRuntime) {
			v.TransportPolicy.ArtifactOrigins = []string{"https://token@storage.example.com"}
		},
		"duplicate origins": func(v *GitLabActionsRuntime) {
			v.TransportPolicy.ArtifactOrigins = []string{"https://storage.example.com", "https://storage.example.com"}
		},
		"too many origins": func(v *GitLabActionsRuntime) {
			for i := 0; i < 17; i++ {
				v.TransportPolicy.ArtifactOrigins = append(v.TransportPolicy.ArtifactOrigins, fmt.Sprintf("https://storage%d.example.com", i))
			}
		},
		"private key CA": func(v *GitLabActionsRuntime) {
			v.CAPEM = []byte("-----BEGIN PRIVATE KEY-----\nfixture\n-----END PRIVATE KEY-----")
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, _, _, native := gitlabActionsDevelopmentFixture(t)
			mutate(&native)
			if ValidateGitLabActionsRuntime(native) == nil {
				t.Fatal("invalid or unqualified native runtime was accepted")
			}
		})
	}
}

func TestGitLabCandidateRejectsMixedOriginalRegistrationAndSlot(t *testing.T) {
	for _, variant := range []string{"runner name", "runner id", "token", "coordinator", "policy coordinator", "timeout", "expired", "architecture", "application service", "resources"} {
		t.Run(variant, func(t *testing.T) {
			target, s, registration, native := gitlabActionsDevelopmentFixture(t)
			switch variant {
			case "runner name":
				registration.Name = "hakopod-" + strings.Repeat("f", 32)
			case "runner id":
				registration.RunnerID = "41/other"
			case "token":
				registration.Token += "\n"
			case "coordinator":
				registration.URL = "https://other.example.com"
			case "policy coordinator":
				native.TransportPolicy.CoordinatorURL = "https://other.example.com"
			case "timeout":
				registration.TimeoutSeconds++
			case "expired":
				expired := time.Now().Add(-time.Minute)
				registration.ExpiresAt = &expired
			case "architecture":
				native.Images.Architecture = "arm64"
			case "application service":
				delete(target.Spec.Services, "runner")
			case "resources":
				s.Resources.MemoryLimit = "128Mi"
			}
			if _, err := gitlabActionsPod(target, "runner", gitlabActionsFixtureSlot, s, registration, native); err == nil || strings.Contains(err.Error(), gitlabActionsFixtureToken) {
				t.Fatal("mixed registration was accepted or leaked its credential")
			}
		})
	}
}

func TestGitLabCandidateSaveStartIsImmutableOwnedAndIdempotent(t *testing.T) {
	target, s, registration, native := gitlabActionsDevelopmentFixture(t)
	c, kube := gitlabActionsDevelopmentClient(t, target)
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		if err := c.SaveGitLabActionsConfig(ctx, target, "runner", gitlabActionsFixtureSlot, s, registration, native); err != nil {
			t.Fatal(err)
		}
		if err := c.StartGitLabActionsPod(ctx, target, "runner", gitlabActionsFixtureSlot, s, registration, native); err != nil {
			t.Fatal(err)
		}
		// A scale-only pool revision does not change this slot's saved config.
		target.Revision++
	}
	secret, _ := kube.CoreV1().Secrets(Namespace(target.ApplicationID)).Get(ctx, "actions-"+gitlabActionsFixtureSlot, metav1.GetOptions{})
	policy, _ := kube.CoreV1().ConfigMaps(Namespace(target.ApplicationID)).Get(ctx, secret.Name, metav1.GetOptions{})
	for _, object := range []metav1.Object{secret, policy} {
		if owned(object, target) != nil || object.GetLabels()[gitlabActionsProviderLabel] != "gitlab" || object.GetLabels()[gitlabActionsSlotLabel] != gitlabActionsFixtureSlot || object.GetLabels()[serviceKey] != "runner" {
			t.Fatal("native artifacts lost scoped ownership")
		}
	}
	if secret.Immutable == nil || !*secret.Immutable || policy.Immutable == nil || !*policy.Immutable {
		t.Fatal("native artifacts are mutable")
	}
	if _, err := spec.Normalize(target.Spec); err != nil {
		t.Fatal("valid native provider configuration could not be normalized", err)
	}
	// Parsing a native provider spec does not approve its execution. The
	// installation resolver must reject this unbound synthetic candidate.
	if _, err := c.ResolveGitLabActions(ctx, target, "runner", s); err == nil {
		t.Fatal("candidate artifacts accidentally enabled unqualified provider execution")
	}
}

func TestGitLabCandidateStartRefusesChangedArtifactsAndMissingIsolation(t *testing.T) {
	for _, variant := range []string{"secret", "policy", "owner", "namespace", "network", "overhead"} {
		t.Run(variant, func(t *testing.T) {
			target, s, registration, native := gitlabActionsDevelopmentFixture(t)
			c, kube := gitlabActionsDevelopmentClient(t, target)
			ctx, ns, name := context.Background(), Namespace(target.ApplicationID), "actions-"+gitlabActionsFixtureSlot
			if err := c.SaveGitLabActionsConfig(ctx, target, "runner", gitlabActionsFixtureSlot, s, registration, native); err != nil {
				t.Fatal(err)
			}
			switch variant {
			case "secret":
				v, _ := kube.CoreV1().Secrets(ns).Get(ctx, name, metav1.GetOptions{})
				v.Data["config.toml"] = []byte("changed")
				_, _ = kube.CoreV1().Secrets(ns).Update(ctx, v, metav1.UpdateOptions{})
			case "policy":
				v, _ := kube.CoreV1().ConfigMaps(ns).Get(ctx, name, metav1.GetOptions{})
				v.Data["transport-policy.json"] = "{}"
				_, _ = kube.CoreV1().ConfigMaps(ns).Update(ctx, v, metav1.UpdateOptions{})
			case "owner":
				v, _ := kube.CoreV1().ConfigMaps(ns).Get(ctx, name, metav1.GetOptions{})
				v.Labels[ownerKey] = "other"
				_, _ = kube.CoreV1().ConfigMaps(ns).Update(ctx, v, metav1.UpdateOptions{})
			case "namespace":
				v, _ := kube.CoreV1().Namespaces().Get(ctx, ns, metav1.GetOptions{})
				v.Labels["hakopod.io/workload-kind"] = "ordinary"
				_, _ = kube.CoreV1().Namespaces().Update(ctx, v, metav1.UpdateOptions{})
			case "network":
				_ = kube.NetworkingV1().NetworkPolicies(ns).Delete(ctx, "hakopod-default-deny", metav1.DeleteOptions{})
			case "overhead":
				v, _ := kube.NodeV1().RuntimeClasses().Get(ctx, ActionsRuntime, metav1.GetOptions{})
				v.Overhead.PodFixed[corev1.ResourceMemory] = resource.MustParse("1Gi")
				_, _ = kube.NodeV1().RuntimeClasses().Update(ctx, v, metav1.UpdateOptions{})
			}
			if err := c.StartGitLabActionsPod(ctx, target, "runner", gitlabActionsFixtureSlot, s, registration, native); err == nil {
				t.Fatal("changed runtime or isolation was accepted")
			}
			if _, err := kube.CoreV1().Pods(ns).Get(ctx, name, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
				t.Fatal("unsafe pod was created")
			}
		})
	}
}

func TestGitLabCandidateFencesEachWriteAndRedactsSecretAPIErrors(t *testing.T) {
	target, s, registration, native := gitlabActionsDevelopmentFixture(t)
	c, kube := gitlabActionsDevelopmentClient(t, target)
	ctx := context.Background()
	denied := errors.New("lease revoked")
	steps := 0
	target.BeforeStep = func(context.Context) error {
		steps++
		if steps == 2 {
			return denied
		}
		return nil
	}
	if err := c.SaveGitLabActionsConfig(ctx, target, "runner", gitlabActionsFixtureSlot, s, registration, native); !errors.Is(err, denied) {
		t.Fatal("second artifact write was not fenced", err)
	}
	if _, err := kube.CoreV1().ConfigMaps(Namespace(target.ApplicationID)).Get(ctx, "actions-"+gitlabActionsFixtureSlot, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatal("revoked lease wrote transport policy")
	}
	target.BeforeStep = nil
	if err := c.SaveGitLabActionsConfig(ctx, target, "runner", gitlabActionsFixtureSlot, s, registration, native); err != nil {
		t.Fatal("partial artifact creation was not recoverable", err)
	}
	target.BeforeStep = func(context.Context) error { return denied }
	if err := c.StartGitLabActionsPod(ctx, target, "runner", gitlabActionsFixtureSlot, s, registration, native); !errors.Is(err, denied) {
		t.Fatal("pod creation was not fenced", err)
	}
	other, otherKube := gitlabActionsDevelopmentClient(t, target)
	target.BeforeStep = nil
	otherKube.PrependReactor("create", "secrets", func(clienttesting.Action) (bool, k8sruntime.Object, error) {
		return true, nil, fmt.Errorf("admission echoed %s", registration.Token)
	})
	if err := other.SaveGitLabActionsConfig(ctx, target, "runner", gitlabActionsFixtureSlot, s, registration, native); err == nil || strings.Contains(err.Error(), registration.Token) {
		t.Fatal("secret API diagnostics leaked private request material")
	}
}

func TestGitLabCandidatePolicyCleanupWaitsForOwnedPodAbsence(t *testing.T) {
	target, s, registration, native := gitlabActionsDevelopmentFixture(t)
	c, kube := gitlabActionsDevelopmentClient(t, target)
	ctx := context.Background()
	if err := c.SaveGitLabActionsConfig(ctx, target, "runner", gitlabActionsFixtureSlot, s, registration, native); err != nil {
		t.Fatal(err)
	}
	if err := c.StartGitLabActionsPod(ctx, target, "runner", gitlabActionsFixtureSlot, s, registration, native); err != nil {
		t.Fatal(err)
	}
	if err := c.DeleteGitLabActionsPolicy(ctx, target, gitlabActionsFixtureSlot); err == nil {
		t.Fatal("native policy was deleted while runner existed")
	}
	if err := c.DeleteGitLabActionsConfig(ctx, target, gitlabActionsFixtureSlot); err == nil {
		t.Fatal("native registration was deleted while runner existed")
	}
	if _, err := c.DeleteActionsPod(ctx, target, gitlabActionsFixtureSlot); err != nil {
		t.Fatal(err)
	}
	if err := c.DeleteGitLabActionsPolicy(ctx, target, gitlabActionsFixtureSlot); err != nil {
		t.Fatal(err)
	}
	if _, err := kube.CoreV1().ConfigMaps(Namespace(target.ApplicationID)).Get(ctx, "actions-"+gitlabActionsFixtureSlot, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatal("unused policy was retained after owned pod absence")
	}
	if err := c.DeleteGitLabActionsPolicy(ctx, target, gitlabActionsFixtureSlot); err != nil {
		t.Fatal("absent policy cleanup is not idempotent")
	}
	if err := c.DeleteGitLabActionsConfig(ctx, target, gitlabActionsFixtureSlot); err != nil {
		t.Fatal("registration cleanup did not follow pod absence", err)
	}
	if _, err := kube.CoreV1().Secrets(Namespace(target.ApplicationID)).Get(ctx, "actions-"+gitlabActionsFixtureSlot, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatal("private registration remained after completed cleanup")
	}
}

func TestGitLabCandidateCleanupRequiresObservedArtifactAbsence(t *testing.T) {
	for _, kind := range []string{"secrets", "configmaps"} {
		t.Run(kind, func(t *testing.T) {
			target, s, registration, native := gitlabActionsDevelopmentFixture(t)
			c, kube := gitlabActionsDevelopmentClient(t, target)
			ctx := context.Background()
			if err := c.SaveGitLabActionsConfig(ctx, target, "runner", gitlabActionsFixtureSlot, s, registration, native); err != nil {
				t.Fatal(err)
			}
			remove := c.DeleteGitLabActionsPolicy
			if kind == "secrets" {
				remove = c.DeleteGitLabActionsConfig
			}
			// Accepted deletes can be held by finalizers. Keep the tracker object
			// to prove the controller cannot confuse acknowledgement with absence.
			kube.PrependReactor("delete", kind, func(clienttesting.Action) (bool, k8sruntime.Object, error) { return true, nil, nil })
			if err := remove(ctx, target, gitlabActionsFixtureSlot); err == nil || strings.Contains(err.Error(), registration.Token) {
				t.Fatal("accepted delete without observed absence completed cleanup")
			}
			kube.ReactionChain = kube.ReactionChain[1:]
			if err := remove(ctx, target, gitlabActionsFixtureSlot); err != nil {
				t.Fatal("artifact cleanup did not resume", err)
			}
		})
	}
}

func TestGitLabCandidateRegistrationCleanupRefusesAnotherProvider(t *testing.T) {
	target, s, registration, native := gitlabActionsDevelopmentFixture(t)
	c, kube := gitlabActionsDevelopmentClient(t, target)
	ctx := context.Background()
	if err := c.SaveGitLabActionsConfig(ctx, target, "runner", gitlabActionsFixtureSlot, s, registration, native); err != nil {
		t.Fatal(err)
	}
	api := kube.CoreV1().Secrets(Namespace(target.ApplicationID))
	secret, err := api.Get(ctx, "actions-"+gitlabActionsFixtureSlot, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	secret.Labels[gitlabActionsProviderLabel] = "another-provider"
	if _, err = api.Update(ctx, secret, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err = c.DeleteGitLabActionsConfig(ctx, target, gitlabActionsFixtureSlot); err == nil {
		t.Fatal("cleanup removed a different provider's registration")
	}
	if _, err = api.Get(ctx, secret.Name, metav1.GetOptions{}); err != nil {
		t.Fatal("foreign registration was not preserved")
	}
}

func TestGitLabCandidatePodIdempotenceRejectsChangedExecutableOrPrivilege(t *testing.T) {
	for name, change := range map[string]func(*corev1.Pod){
		"image":   func(p *corev1.Pod) { p.Spec.Containers[0].Image = "untrusted:latest" },
		"command": func(p *corev1.Pod) { p.Spec.Containers[0].Command = []string{"sh"} },
		"credential mount": func(p *corev1.Pod) {
			p.Spec.InitContainers[1].VolumeMounts = append(p.Spec.InitContainers[1].VolumeMounts, corev1.VolumeMount{Name: "registration", MountPath: "/private"})
		},
		"host network":       func(p *corev1.Pod) { p.Spec.HostNetwork = true },
		"privileged daemon":  func(p *corev1.Pod) { p.Spec.InitContainers[1].SecurityContext.Privileged = ptr(true) },
		"unbounded lifetime": func(p *corev1.Pod) { p.Spec.ActiveDeadlineSeconds = nil },
		"service account":    func(p *corev1.Pod) { p.Spec.AutomountServiceAccountToken = ptr(true) },
	} {
		t.Run(name, func(t *testing.T) {
			target, s, registration, native := gitlabActionsDevelopmentFixture(t)
			pod, err := gitlabActionsPod(target, "runner", gitlabActionsFixtureSlot, s, registration, native)
			if err != nil {
				t.Fatal(err)
			}
			changed := pod.DeepCopy()
			change(changed)
			if gitlabActionsPodMatches(changed, pod) {
				t.Fatal("idempotent start accepted changed runtime boundary")
			}
		})
	}
}
