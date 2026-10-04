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
)

func TestPlatformRuntimeMutationResumesOnlyExactJournaledWrite(t *testing.T) {
	for _, tamper := range []string{"none", "spec", "generation", "uid", "token"} {
		t.Run(tamper, func(t *testing.T) {
			ctx := context.Background()
			op := supabaseTestOperation(1)
			ns := supabaseTestNamespace(op)
			live := &appsv1.Deployment{ObjectMeta: supabaseTestMeta(op, ns, "supabase-auth"), Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "auth", Image: "image:v1"}}}}}}
			desired := live.DeepCopy()
			desired.Spec.Template.Spec.Containers[0].Image = "image:v2"
			state := newFakeSupabaseStore()
			claim := store.PlatformResourceClaim{PlatformID: op.PlatformID, PlatformRevision: op.Revision, Component: "deployment.supabase-auth", Kind: "runtime_component", ResourceID: string(live.UID), ImmutableGeneration: 1, OwnerOperationID: op.ID}
			current := map[string]store.PlatformResourceClaim{claim.Component: claim}
			writes := 0
			update := func(object runtime.Object, options metav1.UpdateOptions) (runtime.Object, error) {
				value := object.(*appsv1.Deployment).DeepCopy()
				// Model API defaulting that is absent from the reviewed renderer input.
				if value.Spec.RevisionHistoryLimit == nil {
					n := int32(10)
					value.Spec.RevisionHistoryLimit = &n
				}
				value.Generation = live.Generation
				if !reflect.DeepEqual(value.Spec, live.Spec) || !reflect.DeepEqual(value.Annotations, live.Annotations) {
					value.Generation++
				}
				if len(options.DryRun) == 0 {
					writes++
					live = value.DeepCopy()
				}
				return value, nil
			}
			lost := errors.New("commit response unavailable")
			state.completeMutationErr = lost
			err := applyPlatformRuntimeMutation(ctx, state, op, "deployment", live, desired.DeepCopy(), nil, current, func() error { return nil }, update)
			if !errors.Is(err, lost) || writes != 1 || current[claim.Component].ImmutableGeneration != 1 {
				t.Fatal("write was not left recoverable", err, writes)
			}
			switch tamper {
			case "spec":
				live.Spec.Template.Spec.Containers[0].Image = "foreign"
			case "generation":
				live.Generation++
			case "uid":
				live.UID = "foreign"
			case "token":
				live.Annotations[platformRuntimeTransitionAnnotation] = "foreign"
			}
			state.completeMutationErr = nil
			err = applyPlatformRuntimeMutation(ctx, state, op, "deployment", live, desired.DeepCopy(), nil, current, func() error { return nil }, update)
			if tamper != "none" {
				if err == nil || current[claim.Component].ImmutableGeneration != 1 || writes != 1 {
					t.Fatal("foreign mutation was adopted", err)
				}
				return
			}
			if err != nil || current[claim.Component].ImmutableGeneration != 2 || writes != 1 {
				t.Fatal("exact write did not resume without a second rollout", err, writes)
			}
			if err = verifySupabaseClaimedUID("deployment", live, current); err != nil {
				t.Fatal(err)
			}
			live.Generation++
			if err = verifySupabaseClaimedUID("deployment", live, current); err == nil {
				t.Fatal("readiness accepted an unclaimed generation")
			}
		})
	}
}

func TestPlatformRuntimeMutationRefusesUnjournaledGeneration(t *testing.T) {
	op := supabaseTestOperation(1)
	object := &appsv1.StatefulSet{ObjectMeta: supabaseTestMeta(op, supabaseTestNamespace(op), "supabase-database")}
	object.Generation = 2
	claim := store.PlatformResourceClaim{PlatformID: op.PlatformID, PlatformRevision: 1, Component: "statefulset.supabase-database", Kind: "runtime_component", ResourceID: string(object.UID), ImmutableGeneration: 1, OwnerOperationID: op.ID}
	current := map[string]store.PlatformResourceClaim{claim.Component: claim}
	calls := 0
	err := applyPlatformRuntimeMutation(context.Background(), newFakeSupabaseStore(), op, "statefulset", object, object.DeepCopy(), nil, current, func() error { return nil }, func(runtime.Object, metav1.UpdateOptions) (runtime.Object, error) { calls++; return nil, nil })
	if err == nil || calls != 0 {
		t.Fatal("unjournaled generation reached the write path")
	}
}

func TestPlatformRuntimeMutationResumesBeforeWriteAndAllowsUnchangedGeneration(t *testing.T) {
	ctx := context.Background()
	op := supabaseTestOperation(1)
	object := &appsv1.StatefulSet{ObjectMeta: supabaseTestMeta(op, supabaseTestNamespace(op), "supabase-database")}
	desired := object.DeepCopy()
	desired.Labels["hakopod.io/revision"] = "reviewed"
	state := newFakeSupabaseStore()
	claim := store.PlatformResourceClaim{PlatformID: op.PlatformID, PlatformRevision: 1, Component: "statefulset.supabase-database", Kind: "runtime_component", ResourceID: string(object.UID), ImmutableGeneration: 1, OwnerOperationID: op.ID}
	current := map[string]store.PlatformResourceClaim{claim.Component: claim}
	calls, writes := 0, 0
	update := func(input runtime.Object, options metav1.UpdateOptions) (runtime.Object, error) {
		value := input.(*appsv1.StatefulSet).DeepCopy()
		value.Generation = 1
		if len(options.DryRun) == 0 {
			writes++
			object = value.DeepCopy()
		}
		return value, nil
	}
	lost := errors.New("lease expired before write")
	err := applyPlatformRuntimeMutation(ctx, state, op, "statefulset", object, desired.DeepCopy(), nil, current, func() error {
		calls++
		if calls == 2 {
			return lost
		}
		return nil
	}, update)
	if !errors.Is(err, lost) || writes != 0 || state.mutations[claim.Component].Token == "" {
		t.Fatal("pre-write failure lost its durable intent", err)
	}
	err = applyPlatformRuntimeMutation(ctx, state, op, "statefulset", object, desired.DeepCopy(), nil, current, func() error { return nil }, update)
	if err != nil || writes != 1 || current[claim.Component].ImmutableGeneration != 1 || state.mutations[claim.Component].Token != "" {
		t.Fatal("metadata-only transition did not resume", err, writes)
	}
}
