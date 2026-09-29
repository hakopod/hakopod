package cluster

import (
	"context"
	"testing"

	"github.com/hakopod/hakopod/internal/spec"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestGitLabExecutionQualificationIsBoundToCoordinatorArchitectureAndCache(t *testing.T) {
	_, _, _, runtime := gitlabActionsDevelopmentFixture(t)
	if err := ValidateGitLabActionsExecution(runtime, false); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*GitLabActionsRuntime){
		"transport alone":              func(r *GitLabActionsRuntime) { r.Images.Execution = nil },
		"wrong architecture":           func(r *GitLabActionsRuntime) { r.Images.Execution.Architecture = "arm64" },
		"wrong coordinator":            func(r *GitLabActionsRuntime) { r.TransportPolicy.CoordinatorURL = "https://gitlab.example.test" },
		"missing credential isolation": func(r *GitLabActionsRuntime) { r.Images.Execution.CredentialIsolation = false },
		"missing drain":                func(r *GitLabActionsRuntime) { r.Images.Execution.Drain = false },
		"missing cleanup":              func(r *GitLabActionsRuntime) { r.Images.Execution.Cleanup = false },
		"missing report":               func(r *GitLabActionsRuntime) { r.Images.ExecutionReportSHA256 = "" },
	} {
		t.Run(name, func(t *testing.T) {
			bad := cloneGitLabRuntime(runtime)
			mutate(&bad)
			if ValidateGitLabActionsExecution(bad, false) == nil {
				t.Fatal("incomplete or unrelated execution proof admitted")
			}
		})
	}
	runtime.Images.CacheProtocolVersion = 1
	runtime.Cache = &GitLabActionsCache{ServerAddress: "cache.example.test", Bucket: "runner-cache", Region: "us-east-1", MaxArchiveBytes: 2 << 20}
	if ValidateGitLabActionsExecution(runtime, true) == nil || ValidateGitLabActionsExecution(runtime, false) != nil {
		t.Fatal("cache proof did not remain optional and separate")
	}
	proof := runtime.Images.Execution
	proof.CacheServerAddress, proof.CacheSaveRestore, proof.CacheSizeLimit, proof.CacheScopeIsolation = runtime.Cache.ServerAddress, true, true, true
	if ValidateGitLabActionsExecution(runtime, true) != nil {
		t.Fatal("complete scoped cache proof rejected")
	}
	runtime.Cache.ServerAddress = "other-cache.example.test"
	if ValidateGitLabActionsExecution(runtime, true) == nil {
		t.Fatal("cache proof crossed storage endpoints")
	}
}

func TestGitLabAdmissionRejectsTransportOnlyBindingBeforePlacementOrRegistration(t *testing.T) {
	client, target, _ := managedActionsNetworkFixture(t)
	client.options.ManagedActions.GitLab[0].Runtime.Images.Execution = nil
	if err := client.validateActionsDelivery(context.Background(), target); err == nil {
		t.Fatal("transport-only installation admitted a native pool")
	}
	capabilities := client.ManagedGitLabCapabilities(target.Project, target.Environment)
	if capabilities.Available || len(capabilities.Bindings) != 0 {
		t.Fatal("transport-only evidence was advertised as execution")
	}
}

func TestGitLabCapabilitiesExposeOnlyExactQualifiedEnvironmentBindings(t *testing.T) {
	client, target, _ := managedActionsNetworkFixture(t)
	capability := client.ManagedGitLabCapabilities(target.Project, target.Environment)
	if !capability.Available || len(capability.Bindings) != 1 || capability.Bindings[0].Application != target.Spec.Name || capability.Bindings[0].Service != "runner" || capability.Bindings[0].Architecture != "amd64" || len(capability.Build.NativeArchitectures) != 1 || capability.Cache.Persistent || capability.Image != "" {
		t.Fatal("capabilities broadened exact runtime qualification")
	}
	if client.ManagedGitLabCapabilities(target.Project, "another").Available {
		t.Fatal("qualification crossed environment boundary")
	}
	service := target.Spec.Services["runner"]
	service.Actions.Cache = &spec.ActionsCache{Credential: "cache"}
	if _, err := client.ResolveGitLabActions(context.Background(), target, "runner", service); err == nil {
		t.Fatal("cache opt-in borrowed execution-only qualification")
	}
}

func TestGitLabSuspensionAndScaleToZeroDoNotRequireRemovedBinding(t *testing.T) {
	client, target, _ := managedActionsNetworkFixture(t)
	client.options.ManagedActions = nil
	for _, suspended := range []bool{false, true} {
		service := target.Spec.Services["runner"]
		service.Suspended = suspended
		if suspended {
			service.Replicas = 1
		} else {
			service.Replicas = 0
		}
		target.Spec.Services["runner"] = service
		if err := client.validateActionsDelivery(context.Background(), target); err != nil {
			t.Fatal("removed qualification prevented a stop-only revision", err)
		}
	}
}

func TestGitLabImageResolutionPreservesQualifiedPairWithoutWorkloadCredentials(t *testing.T) {
	client, target, _ := managedActionsNetworkFixture(t)
	node, err := client.kube.CoreV1().Nodes().Get(context.Background(), "development-node", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	node.Status.NodeInfo.Architecture = "amd64"
	if _, err := client.kube.CoreV1().Nodes().Update(context.Background(), node, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	resolved, err := client.ResolveScoped(context.Background(), target.Spec, target.Project, target.Environment)
	if err != nil {
		t.Fatal(err)
	}
	service := resolved.Services["runner"]
	if service.Image != target.Spec.Services["runner"].Image || service.RegistryCredential != "" {
		t.Fatal("qualified manager changed or acquired ordinary registry credentials")
	}
}
