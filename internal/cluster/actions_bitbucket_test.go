package cluster

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/hakopod/hakopod/internal/actions"
	"github.com/hakopod/hakopod/internal/spec"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const bitbucketActionsFixtureSlot = "1234567890abcdef1234567890abcdef"

// Synthetic fixtures check the Kubernetes contract, not native compatibility.
func bitbucketActionsDevelopmentFixture(t *testing.T) (Target, spec.Service, actions.BitbucketManagerConfig, BitbucketActionsRuntime) {
	t.Helper()
	target := runnerTarget(t)
	s := target.Spec.Services["runner"]
	s.Architecture = "amd64"
	s.Resources = &spec.Resources{CPURequest: "1", CPULimit: "2", MemoryRequest: "8Gi", MemoryLimit: "8Gi"}
	s.Actions = &spec.Actions{Provider: actions.ProviderBitbucket, Bitbucket: &actions.BitbucketTarget{Workspace: "{aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa}", Repository: "{bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb}"}, Credential: "bitbucket-management", Labels: []string{"native"}, TimeoutMinutes: 15, WorkspaceSizeGiB: 8}
	registration := actions.BitbucketManagerConfig{SchemaVersion: 1, Target: *s.Actions.Bitbucket, RunnerID: "{cccccccc-cccc-4ccc-8ccc-cccccccccccc}", Name: "hakopod-" + bitbucketActionsFixtureSlot, OAuthClientID: "fixture-oauth-client", OAuthSecret: "fixture-private-oauth-secret"}
	runtime := BitbucketActionsRuntime{ManagerImage: "docker-public.packages.atlassian.com/sox/atlassian/bitbucket-pipelines-runner@sha256:" + strings.Repeat("a", 64), Architecture: "amd64"}
	s.Image = runtime.ManagerImage
	target.Spec.Services["runner"] = s
	return target, s, registration, runtime
}

func TestBitbucketCandidateKeepsOAuthOutsideJobAccessibleStorage(t *testing.T) {
	target, s, registration, runtime := bitbucketActionsDevelopmentFixture(t)
	pod, err := BitbucketActionsPod(target, "runner", bitbucketActionsFixtureSlot, s, registration, runtime)
	if err != nil {
		t.Fatal(err)
	}
	secret, err := bitbucketActionsSecret(target, "runner", bitbucketActionsFixtureSlot, s, registration, runtime)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(pod)
	if strings.Contains(string(encoded), registration.OAuthSecret) || strings.Contains(string(encoded), registration.OAuthClientID) || strings.Contains(string(encoded), s.Actions.Credential) {
		t.Fatal("private bootstrap or management credentials entered the public pod")
	}
	if string(secret.Data["oauth_client_secret"]) != registration.OAuthSecret || string(secret.Data["oauth_client_id"]) != registration.OAuthClientID || len(secret.Data) != 2 || secret.Immutable == nil || !*secret.Immutable {
		t.Fatal("private native credential was not stored as immutable isolated bootstrap material")
	}
	if *pod.Spec.RuntimeClassName != ActionsRuntime || *pod.Spec.AutomountServiceAccountToken || *pod.Spec.ShareProcessNamespace || pod.Spec.HostNetwork || pod.Spec.HostPID || pod.Spec.HostIPC || pod.Spec.RestartPolicy != corev1.RestartPolicyNever || pod.Spec.ActiveDeadlineSeconds != nil || pod.Annotations["hakopod.io/actions-lifecycle"] != "dedicated" {
		t.Fatal("candidate lost its isolation or invented disposable semantics")
	}
	for _, volume := range pod.Spec.Volumes {
		if volume.HostPath != nil || volume.Projected != nil || volume.PersistentVolumeClaim != nil || volume.Secret != nil || volume.EmptyDir == nil || volume.EmptyDir.SizeLimit == nil {
			t.Fatal("candidate storage escaped the bounded private sandbox")
		}
		if volume.Name != "manager-state" && volume.EmptyDir.Medium != "" {
			t.Fatal("shared Docker path must be disk-backed for gVisor socket sharing")
		}
	}
	for _, container := range append(pod.Spec.InitContainers, pod.Spec.Containers...) {
		if container.SecurityContext == nil || (container.SecurityContext.Privileged != nil && *container.SecurityContext.Privileged) {
			t.Fatal("candidate enabled outer privileged mode")
		}
		for _, env := range container.Env {
			if env.ValueFrom != nil && (container.Name != "runner" || env.ValueFrom.SecretKeyRef == nil || env.ValueFrom.SecretKeyRef.Name != pod.Name) {
				t.Fatal("manager bootstrap credentials escaped to another container")
			}
		}
		for _, mount := range container.VolumeMounts {
			if container.Name == "docker" && mount.Name == "manager-state" {
				t.Fatal("manager-private files are visible to the job daemon")
			}
			if container.Name == "runner" && mount.Name == "docker-data" && (mount.SubPath != "containers" || !mount.ReadOnly) {
				t.Fatal("manager received more than its private read-only Docker log path")
			}
		}
	}
	if strings.Contains(bitbucketActionsDaemon, "--host=tcp") || !strings.Contains(bitbucketActionsDaemon, "--log-opt=max-size=10m") || !strings.Contains(bitbucketActionsDaemon, "--default-pids-limit=512") {
		t.Fatal("private daemon lost socket-only access or resource bounds")
	}
}

func TestBitbucketCandidateResourceSplitFitsDeclaredBudget(t *testing.T) {
	target, s, registration, runtime := bitbucketActionsDevelopmentFixture(t)
	pod, err := BitbucketActionsPod(target, "runner", bitbucketActionsFixtureSlot, s, registration, runtime)
	if err != nil {
		t.Fatal(err)
	}
	profile := spec.EffectiveResources(s)
	for _, bound := range []struct {
		kind                  corev1.ResourceName
		request, limit, extra string
	}{{corev1.ResourceCPU, profile.CPURequest, profile.CPULimit, "100m"}, {corev1.ResourceMemory, profile.MemoryRequest, profile.MemoryLimit, "512Mi"}, {corev1.ResourceEphemeralStorage, "8448Mi", "11Gi", "0"}} {
		for _, limits := range []bool{false, true} {
			total, maximum := resource.MustParse(bound.extra), bound.request
			if limits {
				maximum = bound.limit
			}
			for _, container := range []corev1.Container{pod.Spec.InitContainers[1], pod.Spec.Containers[0]} {
				resources := container.Resources.Requests
				if limits {
					resources = container.Resources.Limits
				}
				total.Add(resources[bound.kind])
			}
			if total.Cmp(resource.MustParse(maximum)) > 0 {
				t.Fatal("candidate exceeded its declared runner budget", bound.kind)
			}
		}
	}
	storage := resource.MustParse("0")
	for _, volume := range pod.Spec.Volumes {
		if volume.EmptyDir.Medium == "" {
			storage.Add(*volume.EmptyDir.SizeLimit)
		}
	}
	if storage.Cmp(resource.MustParse("8Gi")) != 0 {
		t.Fatal("shared private storage did not fit its declared workspace reservation")
	}
}

func TestBitbucketCandidateFailsBeforeClusterWritesForWrongScope(t *testing.T) {
	for _, variant := range []string{"workspace", "architecture", "memory", "mutable image", "different image", "slot", "revision"} {
		t.Run(variant, func(t *testing.T) {
			target, s, registration, runtime := bitbucketActionsDevelopmentFixture(t)
			client, kube := gitlabActionsDevelopmentClient(t, target)
			slot := bitbucketActionsFixtureSlot
			switch variant {
			case "workspace":
				s.Actions.Bitbucket.Repository, registration.Target.Repository = "", ""
			case "architecture":
				runtime.Architecture = "arm64"
			case "memory":
				s.Resources.MemoryRequest = "4Gi"
			case "mutable image":
				runtime.ManagerImage = "docker-public.packages.atlassian.com/sox/atlassian/bitbucket-pipelines-runner:latest"
			case "different image":
				s.Image = "docker-public.packages.atlassian.com/sox/atlassian/bitbucket-pipelines-runner@sha256:" + strings.Repeat("b", 64)
			case "slot":
				slot = "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
			case "revision":
				target.Revision = 0
			}
			target.Spec.Services["runner"] = s
			if err := client.SaveBitbucketActionsConfig(context.Background(), target, "runner", slot, s, registration, runtime); err == nil || len(kube.Actions()) != 0 {
				t.Fatal("invalid runtime reached the Kubernetes API", err)
			}
		})
	}
}

func TestBitbucketCandidateSaveAndStartRequireImmutableOwnedArtifacts(t *testing.T) {
	target, s, registration, runtime := bitbucketActionsDevelopmentFixture(t)
	client, kube := gitlabActionsDevelopmentClient(t, target)
	if err := client.SaveBitbucketActionsConfig(context.Background(), target, "runner", bitbucketActionsFixtureSlot, s, registration, runtime); err != nil {
		t.Fatal(err)
	}
	if err := client.StartBitbucketActionsPod(context.Background(), target, "runner", bitbucketActionsFixtureSlot, s, registration, runtime); err != nil {
		t.Fatal(err)
	}
	if err := client.StartBitbucketActionsPod(context.Background(), target, "runner", bitbucketActionsFixtureSlot, s, registration, runtime); err != nil {
		t.Fatal("idempotent start failed", err)
	}
	secret, err := kube.CoreV1().Secrets(Namespace(target.ApplicationID)).Get(context.Background(), "actions-"+bitbucketActionsFixtureSlot, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	secret.Data["oauth_client_secret"] = []byte("substituted-private-credential")
	if _, err := kube.CoreV1().Secrets(secret.Namespace).Update(context.Background(), secret, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := client.StartBitbucketActionsPod(context.Background(), target, "runner", bitbucketActionsFixtureSlot, s, registration, runtime); err == nil {
		t.Fatal("runtime accepted a changed private registration")
	}
}
