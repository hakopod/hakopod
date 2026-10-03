package cluster

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/managedplatform"
	"github.com/hakopod/hakopod/internal/store"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

type neonEmptyDeleteStore struct {
	*fakeSupabaseOperationStore
	proofCalls int
	revoked    int
}

func (s *neonEmptyDeleteStore) NeonProviderStateEmpty(_ context.Context, op store.ManagedPlatformOperation) (bool, error) {
	s.proofCalls++
	return op.Kind == "delete", nil
}

func (s *neonEmptyDeleteStore) ActivateNeonProxyEndpoint(context.Context, store.ManagedPlatformOperation, store.NeonProxyEndpointRecord) error {
	return nil
}

func (s *neonEmptyDeleteStore) RevokeNeonProxyEndpoint(context.Context, store.ManagedPlatformOperation) error {
	s.revoked++
	return nil
}

func TestNeonUnprovisionedDeleteUsesOwnedNamespaceCleanup(t *testing.T) {
	for _, changedUID := range []bool{false, true} {
		name := "owned namespace"
		if changedUID {
			name = "replaced namespace"
		}
		t.Run(name, func(t *testing.T) {
			op := store.ManagedPlatformOperation{ID: strings.Repeat("5", 32), PlatformID: strings.Repeat("4", 32), Revision: 2, Kind: "delete", Lease: "lease"}
			op.Spec = managedplatform.Spec{
				Kind: "neon",
				Neon: &managedplatform.NeonConfig{ComputeReplicas: 1, Pageservers: 2, Safekeepers: 3},
				Secrets: map[string]managedplatform.SecretReference{
					"controller-auth": {Name: "controller-auth", Revision: 1},
					"compute-auth":    {Name: "compute-auth", Revision: 1},
					"safekeeper-auth": {Name: "safekeeper-auth", Revision: 1},
				},
			}
			ns := supabaseTestNamespace(op)
			state := &neonEmptyDeleteStore{fakeSupabaseOperationStore: newFakeSupabaseStore()}
			uid := string(ns.UID)
			if changedUID {
				uid = "original-namespace-uid"
			}
			state.claims[1] = []store.PlatformResourceClaim{{PlatformID: op.PlatformID, PlatformRevision: 1, Component: "namespace." + ns.Name, Kind: "runtime_component", ResourceID: uid, ImmutableGeneration: 1, OwnerOperationID: strings.Repeat("6", 32)}}
			ca := supabaseGatewayCertificateFixture(t, []string{"neon.test"}, time.Now().Add(-time.Minute), time.Now().Add(time.Hour))["ca.crt"]
			// An accepted snapshot already carries tenant storage authentication.
			computeSnapshot := strings.Replace(validNeonComputeTemplate, `"spec":{`, `"spec":{"storage_auth_token":"fixture-storage-token",`, 1)
			request := NeonRuntimeRequest{
				Operation: op,
				Render:    managedplatform.NeonRenderInput{Spec: op.Spec, PlatformID: op.PlatformID, Revision: op.Revision},
				SecretSnapshots: map[string]map[string][]byte{
					"controller-auth-r1": {"token": []byte("controller-secret-token"), "ca.crt": ca},
					"compute-auth-r1":    {"token": []byte("compute-secret-token"), "ca.crt": ca, "config.json": []byte(computeSnapshot)},
					"safekeeper-auth-r1": {"token": []byte("safekeeper-secret-token"), "ca.crt": ca},
				},
				ProxyEndpoint: managedplatform.NeonProxyBootstrapState{EndpointID: op.PlatformID},
			}
			kube := fake.NewSimpleClientset(ns)
			client := &Client{kube: kube}
			err := client.ReconcileNeonOperation(context.Background(), state, request, nil)
			deletions := 0
			for _, action := range kube.Actions() {
				if action.GetVerb() != "delete" {
					continue
				}
				deletions++
				deletion, ok := action.(ktesting.DeleteAction)
				if !ok || action.GetResource().Resource != "namespaces" || deletion.GetName() != ns.Name {
					t.Fatalf("unexpected cleanup action: %#v", action)
				}
				options := deletion.GetDeleteOptions()
				if options.Preconditions == nil || options.Preconditions.UID == nil || *options.Preconditions.UID != ns.UID || options.PropagationPolicy == nil || *options.PropagationPolicy != metav1.DeletePropagationForeground {
					t.Fatalf("namespace deletion lost ownership preconditions: %#v", options)
				}
			}
			if changedUID {
				if err == nil || deletions != 0 || state.proofCalls != 0 {
					t.Fatalf("replaced namespace reached provider or Kubernetes cleanup: error=%v deletions=%d proofs=%d", err, deletions, state.proofCalls)
				}
				return
			}
			if err != nil || deletions != 1 || state.proofCalls != 1 || state.revoked != 1 || state.heartbeats < 1 {
				t.Fatalf("unprovisioned deletion did not reach fenced namespace cleanup: error=%v deletions=%d proofs=%d revocations=%d heartbeats=%d", err, deletions, state.proofCalls, state.revoked, state.heartbeats)
			}
			if len(state.records) != 1 || state.records[0] != "queued:waiting-delete" {
				t.Fatalf("cleanup claimed completion before namespace absence: %v", state.records)
			}
		})
	}
}
