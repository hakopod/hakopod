package cluster

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/hakopod/hakopod/internal/store"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
)

func (*fakeSupabaseOperationStore) PlatformRuntimeMutationForRepair(context.Context, store.ManagedPlatformOperation, string) (store.PlatformRuntimeRepair, error) {
	return store.PlatformRuntimeRepair{}, nil
}

func (*fakeSupabaseOperationStore) ResolvePlatformRuntimeMutationRepair(context.Context, store.ManagedPlatformOperation, store.PlatformRuntimeRepair, bool) error {
	return store.ErrConflict
}

type terminalRuntimeRepairStore struct {
	*fakeSupabaseOperationStore
	repair     store.PlatformRuntimeRepair
	resolved   int
	applied    bool
	resolveErr error
}

func (s *terminalRuntimeRepairStore) PlatformRuntimeMutationForRepair(context.Context, store.ManagedPlatformOperation, string) (store.PlatformRuntimeRepair, error) {
	return s.repair, nil
}

func (s *terminalRuntimeRepairStore) ResolvePlatformRuntimeMutationRepair(_ context.Context, _ store.ManagedPlatformOperation, repair store.PlatformRuntimeRepair, applied bool) error {
	if s.resolveErr != nil {
		return s.resolveErr
	}
	if repair.Mutation != s.repair.Mutation {
		return store.ErrConflict
	}
	s.resolved++
	s.applied = applied
	s.repair = store.PlatformRuntimeRepair{}
	return nil
}

func terminalRuntimeRepairFixture(t *testing.T, kind string) (*terminalRuntimeRepairStore, store.ManagedPlatformOperation, runtime.Object, runtime.Object, map[string]store.PlatformResourceClaim) {
	t.Helper()
	op := supabaseTestOperation(2)
	meta := supabaseTestMeta(op, supabaseTestNamespace(op), "workload")
	meta.ResourceVersion = "old-version"
	var original, applied runtime.Object
	template := corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "workload", Image: "image:v1"}}}}
	if kind == "deployment" {
		value := &appsv1.Deployment{ObjectMeta: meta, Spec: appsv1.DeploymentSpec{Template: template}}
		original = value
		changed := value.DeepCopy()
		changed.Spec.Template.Spec.Containers[0].Image = "image:v2"
		applied = changed
	} else {
		value := &appsv1.StatefulSet{ObjectMeta: meta, Spec: appsv1.StatefulSetSpec{Template: template}}
		original = value
		changed := value.DeepCopy()
		changed.Spec.Template.Spec.Containers[0].Image = "image:v2"
		applied = changed
	}
	metadata := applied.(metav1.Object)
	metadata.SetGeneration(2)
	metadata.SetAnnotations(map[string]string{platformRuntimeTransitionAnnotation: "0123456789abcdef0123456789abcdef"})
	metadata.SetResourceVersion("applied-version")
	digest, err := platformRuntimeSpecDigest(applied)
	if err != nil {
		t.Fatal(err)
	}
	claim := store.PlatformResourceClaim{PlatformID: op.PlatformID, PlatformRevision: 1, Component: kind + ".workload", Kind: "runtime_component", ResourceID: string(meta.UID), ImmutableGeneration: 1, OwnerOperationID: "terminal-owner"}
	mutation := store.PlatformRuntimeMutation{Component: claim.Component, ResourceID: claim.ResourceID, OldGeneration: 1, NewGeneration: 2, Token: metadata.GetAnnotations()[platformRuntimeTransitionAnnotation], SpecSHA256: digest}
	state := &terminalRuntimeRepairStore{fakeSupabaseOperationStore: newFakeSupabaseStore(), repair: store.PlatformRuntimeRepair{Mutation: mutation, Claim: claim}}
	return state, op, original, applied, map[string]store.PlatformResourceClaim{claim.Component: claim}
}

func TestTerminalPlatformRuntimeRepairAcceptsOnlyExactAppliedWrite(t *testing.T) {
	for _, kind := range []string{"deployment", "statefulset"} {
		for _, tamper := range []string{"none", "uid", "generation", "spec", "token"} {
			t.Run(kind+"/"+tamper, func(t *testing.T) {
				state, op, _, live, prior := terminalRuntimeRepairFixture(t, kind)
				metadata := live.(metav1.Object)
				switch tamper {
				case "uid":
					metadata.SetUID("foreign")
				case "generation":
					metadata.SetGeneration(3)
				case "token":
					metadata.SetAnnotations(nil)
				case "spec":
					switch value := live.(type) {
					case *appsv1.Deployment:
						value.Spec.Template.Spec.Containers[0].Image = "foreign"
					case *appsv1.StatefulSet:
						value.Spec.Template.Spec.Containers[0].Image = "foreign"
					}
				}
				writes := 0
				_, err := repairTerminalPlatformRuntimeMutation(context.Background(), state, op, kind, live, prior, func() error { return nil }, func(runtime.Object, metav1.UpdateOptions) (runtime.Object, error) { writes++; return nil, nil })
				if tamper == "none" {
					if err != nil || !state.applied || state.resolved != 1 || prior[kind+".workload"].ImmutableGeneration != 2 {
						t.Fatal("exact terminal write did not resolve", err)
					}
				} else if err == nil || state.resolved != 0 {
					t.Fatal("changed terminal write was adopted", err)
				}
				if writes != 0 {
					t.Fatal("applied intent unexpectedly wrote Kubernetes")
				}
			})
		}
	}
}

func TestTerminalPlatformRuntimeRepairFencesUnappliedWriteAcrossCrashes(t *testing.T) {
	for _, kind := range []string{"deployment", "statefulset"} {
		for _, failure := range []string{"none", "lost-fence-response", "lost-resolution", "original-wins-cas", "stale-lease"} {
			t.Run(kind+"/"+failure, func(t *testing.T) {
				state, op, live, applied, prior := terminalRuntimeRepairFixture(t, kind)
				original := live.DeepCopyObject()
				lost := errors.New("connection lost")
				writes := 0
				update := func(value runtime.Object, _ metav1.UpdateOptions) (runtime.Object, error) {
					writes++
					if value.(metav1.Object).GetResourceVersion() != live.(metav1.Object).GetResourceVersion() {
						return nil, store.ErrConflict
					}
					if failure == "original-wins-cas" && writes == 1 {
						live = applied.DeepCopyObject()
						return nil, store.ErrConflict
					}
					live = value.DeepCopyObject()
					live.(metav1.Object).SetResourceVersion("fenced-version")
					if failure == "lost-fence-response" && writes == 1 {
						return nil, lost
					}
					return live.DeepCopyObject(), nil
				}
				before := func() error {
					if failure == "stale-lease" {
						return store.ErrConflict
					}
					return nil
				}
				if failure == "lost-resolution" {
					state.resolveErr = lost
				}
				_, err := repairTerminalPlatformRuntimeMutation(context.Background(), state, op, kind, live, prior, before, update)
				if failure == "stale-lease" {
					if err == nil || writes != 0 || state.resolved != 0 {
						t.Fatal("stale lease changed terminal workload", err)
					}
					return
				}
				if failure != "none" {
					if err == nil || state.resolved != 0 {
						t.Fatal("failed repair lost its journal", err)
					}
					state.resolveErr = nil
					// A later reviewed operation can finish an already fenced intent.
					op.Revision++
					_, err = repairTerminalPlatformRuntimeMutation(context.Background(), state, op, kind, live, prior, func() error { return nil }, update)
				}
				if err != nil || writes != 1 || state.resolved != 1 {
					t.Fatal("terminal repair did not resume once", err, writes, state.resolved)
				}
				if failure == "original-wins-cas" {
					if !state.applied || prior[kind+".workload"].ImmutableGeneration != 2 {
						t.Fatal("original CAS winner was not verified")
					}
				} else {
					if state.applied || prior[kind+".workload"].ImmutableGeneration != 1 || live.(metav1.Object).GetGeneration() != 1 {
						t.Fatal("unapplied repair changed generation")
					}
					oldDigest, _ := platformRuntimeSpecDigest(original)
					newDigest, _ := platformRuntimeSpecDigest(live)
					if !reflect.DeepEqual(oldDigest, newDigest) {
						t.Fatal("metadata fence changed the workload spec")
					}
					if original.(metav1.Object).GetResourceVersion() == live.(metav1.Object).GetResourceVersion() {
						t.Fatal("delayed original write was not fenced")
					}
				}
			})
		}
	}
}

func TestTerminalPlatformRuntimeDeletionResolvesOnlyProvenAbsentUID(t *testing.T) {
	for _, absence := range []string{"namespace", "workload", "replacement-uid", "update-missing-workload"} {
		t.Run(absence, func(t *testing.T) {
			state, op, workload, _, prior := terminalRuntimeRepairFixture(t, "deployment")
			op.Kind = "delete"
			ns := supabaseTestNamespace(op)
			namespaceClaim := store.PlatformResourceClaim{PlatformID: op.PlatformID, PlatformRevision: 1, Component: "namespace." + ns.Name, Kind: "runtime_component", ResourceID: string(ns.UID), ImmutableGeneration: 1, OwnerOperationID: "terminal-owner"}
			prior[namespaceClaim.Component] = namespaceClaim
			state.claims[1] = []store.PlatformResourceClaim{state.repair.Claim, namespaceClaim}
			objects := []runtime.Object{}
			if absence != "namespace" {
				objects = append(objects, ns)
			}
			if absence == "replacement-uid" {
				workload.(metav1.Object).SetUID("replacement-uid")
				objects = append(objects, workload)
			}
			kube := fake.NewSimpleClientset(objects...)
			client := &Client{kube: kube}
			var err error
			if absence == "update-missing-workload" {
				op.Kind = "update"
				err = client.repairTerminalPlatformRuntime(context.Background(), state, op, ns, prior, func() error { return nil })
			} else {
				err = client.deleteSupabaseOperation(context.Background(), state, op, func() error { return nil })
			}
			if absence == "replacement-uid" || absence == "update-missing-workload" {
				if err == nil || state.resolved != 0 || state.releases != 0 {
					t.Fatal("unproven absence was accepted", err)
				}
				for _, action := range kube.Actions() {
					if action.GetVerb() == "update" || action.GetVerb() == "delete" {
						t.Fatal("foreign or missing update workload was mutated")
					}
				}
				return
			}
			if err != nil || state.resolved != 1 || state.applied {
				t.Fatal("proven absent terminal workload did not resolve for deletion", err)
			}
			if state.releases < 1 {
				t.Fatal("absent terminal workload claim remained owned")
			}
			if absence == "namespace" {
				if state.releases != 2 || len(state.records) != 1 || state.records[0] != "succeeded:deleted" {
					t.Fatal("absent namespace deletion did not finish", state.records)
				}
			} else {
				deleted := false
				for _, action := range kube.Actions() {
					if action.GetVerb() == "delete" && action.GetResource().Resource == "namespaces" {
						deleted = true
					}
				}
				if !deleted {
					t.Fatal("absent workload stranded namespace deletion")
				}
			}
		})
	}
}
