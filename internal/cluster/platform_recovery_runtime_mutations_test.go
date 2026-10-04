package cluster

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/hakopod/hakopod/internal/platformbackup"
	"github.com/hakopod/hakopod/internal/store"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type recoveryWorkloadStoreFixture struct {
	pending     store.PlatformRuntimeMutation
	generation  int64
	completeErr error
	abandonErr  error
}

func (s *recoveryWorkloadStoreFixture) PlatformRecoveryWorkloadMutation(context.Context, platformbackup.Operation, string, string) (store.PlatformRuntimeMutation, error) {
	return s.pending, nil
}
func (s *recoveryWorkloadStoreFixture) PlatformRecoveryDeploymentGeneration(context.Context, platformbackup.Operation, string, int64) (int64, error) {
	return s.generation, nil
}
func (s *recoveryWorkloadStoreFixture) PreparePlatformRecoveryWorkloadMutation(_ context.Context, _ platformbackup.Operation, _ string, m store.PlatformRuntimeMutation, _ int32, _ int32, _ int64) error {
	if s.pending.Token != "" && s.pending != m {
		return store.ErrConflict
	}
	s.pending = m
	return nil
}
func (s *recoveryWorkloadStoreFixture) CompletePlatformRecoveryWorkloadMutation(_ context.Context, _ platformbackup.Operation, m store.PlatformRuntimeMutation) error {
	if s.completeErr != nil {
		return s.completeErr
	}
	if s.pending != m {
		return store.ErrConflict
	}
	s.pending = store.PlatformRuntimeMutation{}
	s.generation = m.NewGeneration
	return nil
}

func TestNeonRecoveryStatefulSetResumesExactLostWrite(t *testing.T) {
	for _, tamper := range []string{"none", "spec", "generation", "uid", "token"} {
		t.Run(tamper, func(t *testing.T) {
			ctx := context.Background()
			lifecycle := supabaseTestOperation(1)
			op := platformbackup.Operation{ID: "recovery-operation", Lease: "recovery-lease"}
			one, zero := int32(1), int32(0)
			live := &appsv1.StatefulSet{ObjectMeta: supabaseTestMeta(lifecycle, supabaseTestNamespace(lifecycle), "neon-pageserver-0"), Spec: appsv1.StatefulSetSpec{Replicas: &one, Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "pageserver", Image: "image:v1"}}}}}}
			desired := live.DeepCopy()
			desired.Spec.Replicas = &zero
			desired.Spec.Template.Spec.Containers[0].Image = "image:v2"
			state := &recoveryWorkloadStoreFixture{generation: 1, completeErr: errors.New("commit unavailable")}
			writes := 0
			update := func(input *appsv1.StatefulSet, options metav1.UpdateOptions) (*appsv1.StatefulSet, error) {
				value := input.DeepCopy()
				value.Spec.PodManagementPolicy = appsv1.OrderedReadyPodManagement
				value.Generation = live.Generation
				if !reflect.DeepEqual(value.Spec, live.Spec) {
					value.Generation++
				}
				if len(options.DryRun) == 0 {
					writes++
					live = value.DeepCopy()
				}
				return value, nil
			}
			err := applyRecoveryStatefulSetMutation(ctx, state, op, lifecycle.PlatformID, live, desired, 1, func() error { return nil }, update)
			if !errors.Is(err, state.completeErr) || writes != 1 || state.generation != 1 {
				t.Fatal("lost write did not remain journaled", err)
			}
			switch tamper {
			case "spec":
				live.Spec.Template.Spec.Containers[0].Image = "foreign"
			case "generation":
				live.Generation++
			case "uid":
				live.UID = "foreign"
			case "token":
				live.Annotations["hakopod.io/recovery-transition"] = "foreign"
			}
			state.completeErr = nil
			err = applyRecoveryStatefulSetMutation(ctx, state, op, lifecycle.PlatformID, live, desired, 1, func() error { return nil }, update)
			if tamper != "none" {
				if err == nil || writes != 1 || state.generation != 1 {
					t.Fatal("foreign StatefulSet write was adopted", err)
				}
				return
			}
			if err != nil || writes != 1 || state.generation != 2 || state.pending.Token != "" {
				t.Fatal("exact StatefulSet write did not resume", err, writes)
			}
		})
	}
}

func (s *recoveryWorkloadStoreFixture) AbandonUnappliedPlatformRecoveryWorkloadMutation(_ context.Context, _ platformbackup.Operation, _ string, m store.PlatformRuntimeMutation) error {
	if s.abandonErr != nil {
		return s.abandonErr
	}
	if s.pending != m {
		return store.ErrConflict
	}
	s.pending = store.PlatformRuntimeMutation{}
	return nil
}

func TestNeonRecoveryCleanupFencesUnappliedStartIntent(t *testing.T) {
	ctx := context.Background()
	lifecycle := supabaseTestOperation(1)
	op := platformbackup.Operation{ID: "recovery-operation", Lease: "recovery-lease"}
	zero, one := int32(0), int32(1)
	live := &appsv1.StatefulSet{ObjectMeta: supabaseTestMeta(lifecycle, supabaseTestNamespace(lifecycle), "neon-pageserver-0"), Spec: appsv1.StatefulSetSpec{Replicas: &zero, PodManagementPolicy: appsv1.OrderedReadyPodManagement}}
	start := live.DeepCopy()
	start.Spec.Replicas = &one
	state := &recoveryWorkloadStoreFixture{generation: 1}
	writes, starts := 0, 0
	update := func(input *appsv1.StatefulSet, options metav1.UpdateOptions) (*appsv1.StatefulSet, error) {
		if input.ResourceVersion != live.ResourceVersion {
			return nil, errors.New("resourceVersion conflict")
		}
		value := input.DeepCopy()
		value.Generation = live.Generation
		if !reflect.DeepEqual(value.Spec, live.Spec) {
			value.Generation++
		}
		if len(options.DryRun) == 0 {
			writes++
			value.ResourceVersion = "2"
			live = value.DeepCopy()
			if valueOrOne(value.Spec.Replicas) > 0 {
				starts++
			}
		}
		return value, nil
	}
	calls := 0
	lost := errors.New("lost lease before start")
	err := applyRecoveryStatefulSetMutation(ctx, state, op, lifecycle.PlatformID, live, start, 1, func() error {
		calls++
		if calls == 2 {
			return lost
		}
		return nil
	}, update)
	if !errors.Is(err, lost) || writes != 0 || state.pending.Token == "" {
		t.Fatal("start intent was not retained before write", err)
	}
	oldRequest := start.DeepCopy()
	oldRequest.ResourceVersion = live.ResourceVersion
	stop := live.DeepCopy()
	state.abandonErr = errors.New("lost abandon commit response")
	err = applyRecoveryStatefulSetMutation(ctx, state, op, lifecycle.PlatformID, live, stop, 1, func() error { return nil }, update)
	if !errors.Is(err, state.abandonErr) || writes != 1 || starts != 0 {
		t.Fatal("cleanup fence did not survive interruption", err)
	}
	state.abandonErr = nil
	err = applyRecoveryStatefulSetMutation(ctx, state, op, lifecycle.PlatformID, live, stop, 1, func() error { return nil }, update)
	if err != nil || writes != 1 || starts != 0 || state.pending.Token != "" || live.Generation != 1 {
		t.Fatal("cleanup did not fence unapplied start without starting a writer", err, writes, starts)
	}
	if _, err = update(oldRequest, metav1.UpdateOptions{}); err == nil {
		t.Fatal("delayed original write crossed cleanup resourceVersion fence")
	}
}
