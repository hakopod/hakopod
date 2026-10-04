package cluster

import (
	"context"
	"fmt"

	"github.com/hakopod/hakopod/internal/platformbackup"
	"github.com/hakopod/hakopod/internal/store"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type platformRecoveryWorkloadStore interface {
	PlatformRecoveryWorkloadMutation(context.Context, platformbackup.Operation, string, string) (store.PlatformRuntimeMutation, error)
	PreparePlatformRecoveryWorkloadMutation(context.Context, platformbackup.Operation, string, store.PlatformRuntimeMutation, int32, int32, int64) error
	CompletePlatformRecoveryWorkloadMutation(context.Context, platformbackup.Operation, store.PlatformRuntimeMutation) error
	PlatformRecoveryDeploymentGeneration(context.Context, platformbackup.Operation, string, int64) (int64, error)
	AbandonUnappliedPlatformRecoveryWorkloadMutation(context.Context, platformbackup.Operation, string, store.PlatformRuntimeMutation) error
}

func applyRecoveryStatefulSetMutation(ctx context.Context, state platformRecoveryWorkloadStore, op platformbackup.Operation, platformID string, existing, desired *appsv1.StatefulSet, claimedGeneration int64, before func() error, update func(*appsv1.StatefulSet, metav1.UpdateOptions) (*appsv1.StatefulSet, error)) error {
	if err := before(); err != nil {
		return err
	}
	pending, err := state.PlatformRecoveryWorkloadMutation(ctx, op, platformID, existing.Name)
	if err != nil {
		return err
	}
	if pending.Token != "" {
		if pending.ResourceID != string(existing.UID) || pending.Component != "statefulset."+existing.Name {
			return fmt.Errorf("Neon recovery workload identity changed")
		}
		if existing.Annotations["hakopod.io/recovery-transition"] == pending.Token {
			digest, err := platformRuntimeSpecDigest(existing)
			if err != nil {
				return err
			}
			if existing.Generation != pending.NewGeneration || digest != pending.SpecSHA256 {
				return fmt.Errorf("Neon recovery workload differs from its pending write")
			}
			if err = state.CompletePlatformRecoveryWorkloadMutation(ctx, op, pending); err != nil {
				return err
			}
			pending = store.PlatformRuntimeMutation{}
		} else if existing.Generation != pending.OldGeneration {
			return fmt.Errorf("Neon recovery workload changed outside its pending write")
		}
	}
	expected, err := state.PlatformRecoveryDeploymentGeneration(ctx, op, existing.Name, claimedGeneration)
	if err != nil {
		return err
	}
	if existing.Generation != expected {
		return fmt.Errorf("Neon recovery workload generation changed")
	}
	desired = desired.DeepCopy()
	desired.UID, desired.ResourceVersion = existing.UID, existing.ResourceVersion
	target := valueOrOne(desired.Spec.Replicas)
	token := pending.Token
	if token == "" {
		token = recoveryTransitionToken(op.ID, existing.Name, existing.Generation, target)
	}
	if desired.Annotations == nil {
		desired.Annotations = map[string]string{}
	}
	desired.Annotations["hakopod.io/recovery-transition"] = token
	normalized, err := update(desired, metav1.UpdateOptions{DryRun: []string{metav1.DryRunAll}})
	if err != nil {
		return err
	}
	digest, err := platformRuntimeSpecDigest(normalized)
	if err != nil {
		return err
	}
	oldDigest, err := platformRuntimeSpecDigest(existing)
	if err != nil {
		return err
	}
	if pending.Token == "" && digest == oldDigest {
		return nil
	}
	mutation := store.PlatformRuntimeMutation{Component: "statefulset." + existing.Name, ResourceID: string(existing.UID), OldGeneration: existing.Generation, NewGeneration: normalized.Generation, Token: token, SpecSHA256: digest}
	if pending.Token != "" && pending != mutation {
		// Cleanup may request a stopped workload while an earlier start intent has
		// not been applied. Fence delayed writes before abandoning that intent.
		fenced := existing.DeepCopy()
		alreadyFenced := fenced.Annotations["hakopod.io/recovery-unapplied"] == pending.Token
		if !alreadyFenced {
			if fenced.Annotations == nil {
				fenced.Annotations = map[string]string{}
			}
			fenced.Annotations["hakopod.io/recovery-unapplied"] = pending.Token
			if err = before(); err != nil {
				return err
			}
			fenced, err = update(fenced, metav1.UpdateOptions{})
			if err != nil {
				return err
			}
		}
		fencedDigest, err := platformRuntimeSpecDigest(fenced)
		if err != nil {
			return err
		}
		if !alreadyFenced && fenced.ResourceVersion == existing.ResourceVersion || fenced.UID != existing.UID || fenced.Generation != pending.OldGeneration || fencedDigest != oldDigest || fenced.Annotations["hakopod.io/recovery-unapplied"] != pending.Token || fenced.Annotations["hakopod.io/recovery-transition"] == pending.Token {
			return fmt.Errorf("Neon recovery unapplied-write fence changed")
		}
		if err = state.AbandonUnappliedPlatformRecoveryWorkloadMutation(ctx, op, platformID, pending); err != nil {
			return err
		}
		return applyRecoveryStatefulSetMutation(ctx, state, op, platformID, fenced, desired, claimedGeneration, before, update)
	}
	if err = state.PreparePlatformRecoveryWorkloadMutation(ctx, op, platformID, mutation, valueOrOne(existing.Spec.Replicas), target, claimedGeneration); err != nil {
		return err
	}
	if err = before(); err != nil {
		return err
	}
	updated, err := update(desired, metav1.UpdateOptions{})
	if err != nil {
		return err
	}
	observedDigest, err := platformRuntimeSpecDigest(updated)
	if err != nil {
		return err
	}
	if string(updated.UID) != mutation.ResourceID || updated.Generation != mutation.NewGeneration || updated.Annotations["hakopod.io/recovery-transition"] != mutation.Token || observedDigest != mutation.SpecSHA256 {
		return fmt.Errorf("Neon recovery workload write changed from its durable intent")
	}
	return state.CompletePlatformRecoveryWorkloadMutation(ctx, op, mutation)
}
