package cluster

import (
	"context"
	"testing"

	"github.com/hakopod/hakopod/internal/managedplatform"
	"github.com/hakopod/hakopod/internal/store"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

type neonPendingDeleteStore struct {
	*fakeSupabaseOperationStore
	events []string
}

func (s *neonPendingDeleteStore) CancelPlatformResourceIntent(_ context.Context, _ store.ManagedPlatformOperation, intent store.PlatformResourceIntent) error {
	s.events = append(s.events, "cancel:"+intent.Component)
	s.cancels++
	items := s.intents[intent.PlatformRevision]
	kept := items[:0]
	for _, item := range items {
		if item.ID != intent.ID {
			kept = append(kept, item)
		}
	}
	s.intents[intent.PlatformRevision] = kept
	return nil
}

func TestNeonPendingTimelineCancellationPrecedesSharedNamespaceCleanup(t *testing.T) {
	ctx := context.Background()
	op := supabaseTestOperation(2)
	op.Kind = "delete"
	op.Spec.Kind = "neon"
	ns := supabaseTestNamespace(op)
	createOperationID := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	timelineIntent := store.PlatformResourceIntent{
		ID:               "cccccccccccccccccccccccccccccccc",
		PlatformID:       op.PlatformID,
		PlatformRevision: 1,
		Component:        "timeline",
		Kind:             "neon_timeline",
		ExternalKey:      "dddddddddddddddddddddddddddddddd/eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee",
		OwnerOperationID: createOperationID,
	}
	state := &neonPendingDeleteStore{fakeSupabaseOperationStore: newFakeSupabaseStore()}
	state.intents[1] = []store.PlatformResourceIntent{timelineIntent}
	state.claims[1] = []store.PlatformResourceClaim{
		{PlatformID: op.PlatformID, PlatformRevision: 1, Component: "tenant", Kind: "neon_tenant", ResourceID: "tenant", ImmutableGeneration: 1, OwnerOperationID: createOperationID},
		{PlatformID: op.PlatformID, PlatformRevision: 1, Component: supabaseClaimKey("namespace", ns.Name), Kind: "runtime_component", ResourceID: string(ns.UID), ImmutableGeneration: 1, OwnerOperationID: createOperationID},
	}
	kube := fake.NewSimpleClientset(ns)
	kube.PrependReactor("delete", "namespaces", func(action ktesting.Action) (bool, runtime.Object, error) {
		state.events = append(state.events, "delete:namespace")
		return false, nil, nil
	})
	client := &Client{kube: kube}

	// Durable Neon provider deletion owns the undotted timeline intent. It
	// cancels that intent only after the owned parent has disappeared, before
	// handing the operation to the shared Kubernetes deletion path.
	adapter := neonLifecycleAdapter{state: state, op: op}
	if err := adapter.Cancel(ctx, managedplatform.DurableResourceIntent{
		ID: timelineIntent.ID, PlatformID: timelineIntent.PlatformID,
		PlatformRevision: timelineIntent.PlatformRevision, Component: timelineIntent.Component,
		Kind: timelineIntent.Kind, ExternalKey: timelineIntent.ExternalKey,
		OwnerOperationID: timelineIntent.OwnerOperationID,
	}); err != nil {
		t.Fatal(err)
	}
	if err := client.deleteSupabaseOperation(ctx, state, op, func() error { return state.HeartbeatManagedPlatformOperation(ctx, op) }); err != nil {
		t.Fatal(err)
	}
	if len(state.events) != 2 || state.events[0] != "cancel:timeline" || state.events[1] != "delete:namespace" {
		t.Fatalf("provider intent cancellation did not precede namespace deletion: %#v", state.events)
	}
	if state.cancels != 1 || len(state.intents[1]) != 0 || state.releases != 0 || len(state.records) != 1 || state.records[0] != "queued:waiting-delete" {
		t.Fatalf("first deletion attempt lost durable handoff state: cancels=%d intents=%#v releases=%d records=%#v", state.cancels, state.intents[1], state.releases, state.records)
	}

	if _, err := kube.CoreV1().Namespaces().Get(ctx, ns.Name, metav1.GetOptions{}); err == nil {
		t.Fatal("fake namespace deletion did not remove the namespace")
	}
	if err := client.deleteSupabaseOperation(ctx, state, op, func() error { return state.HeartbeatManagedPlatformOperation(ctx, op) }); err != nil {
		t.Fatal(err)
	}
	if state.cancels != 1 || len(state.intents[1]) != 0 || state.releases != 2 || state.records[len(state.records)-1] != "succeeded:deleted" {
		t.Fatalf("namespace-absent retry leaked provider intent or claims: cancels=%d intents=%#v releases=%d records=%#v", state.cancels, state.intents[1], state.releases, state.records)
	}
}
