package cluster

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/hakopod/hakopod/internal/store"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

const platformRuntimeUnappliedLabel = "hakopod.io/runtime-unapplied"

// Resolve all terminal writes before rendering or deleting. This also covers
// workloads removed from the newly reviewed component inventory.
func (c *Client) repairTerminalPlatformRuntime(ctx context.Context, state ManagedPlatformOperationStore, op store.ManagedPlatformOperation, ns *corev1.Namespace, prior map[string]store.PlatformResourceClaim, before func() error) error {
	if op.Maintenance || op.Revision < 2 {
		return nil
	}
	if len(prior) > maxSupabaseRuntimeObjects {
		return fmt.Errorf("terminal platform runtime repair inventory exceeds its bound")
	}
	components := make([]string, 0, len(prior))
	for component := range prior {
		if strings.HasPrefix(component, "deployment.") || strings.HasPrefix(component, "statefulset.") {
			components = append(components, component)
		}
	}
	sort.Strings(components)
	for _, component := range components {
		repair, err := state.PlatformRuntimeMutationForRepair(ctx, op, component)
		if err != nil {
			return err
		}
		if repair.Mutation.Token == "" {
			continue
		}
		object, err := c.supabaseClaimedObject(ctx, ns.Name, component)
		if apierrors.IsNotFound(err) && op.Kind == "delete" {
			if err = resolveAbsentTerminalPlatformRuntimeMutation(ctx, state, op, component, prior); err != nil {
				return err
			}
			continue
		}
		if err != nil {
			return err
		}
		if err = verifySupabaseOwned(object, op.PlatformID, ns.UID); err != nil {
			return err
		}
		if _, err = c.repairTerminalPlatformDeletionMutation(ctx, state, op, component, object, prior, before); err != nil {
			return err
		}
	}
	return nil
}

// A Kubernetes Update cannot create an absent object. A caller may use this
// only after GET returns NotFound for the workload or its whole namespace.
// Deletion can then release the original UID; updates never recreate it here.
func resolveAbsentTerminalPlatformRuntimeMutation(ctx context.Context, state ManagedPlatformOperationStore, op store.ManagedPlatformOperation, component string, prior map[string]store.PlatformResourceClaim) error {
	if op.Kind != "delete" {
		return store.ErrConflict
	}
	if !strings.HasPrefix(component, "deployment.") && !strings.HasPrefix(component, "statefulset.") {
		return nil
	}
	repair, err := state.PlatformRuntimeMutationForRepair(ctx, op, component)
	if err != nil || repair.Mutation.Token == "" {
		return err
	}
	claim, ok := prior[component]
	if !ok || claim.PlatformRevision != repair.Claim.PlatformRevision || claim.OwnerOperationID != repair.Claim.OwnerOperationID || claim.ResourceID != repair.Mutation.ResourceID || claim.ImmutableGeneration != repair.Mutation.OldGeneration {
		return fmt.Errorf("absent terminal platform workload ownership changed")
	}
	return state.ResolvePlatformRuntimeMutationRepair(ctx, op, repair, false)
}

func repairTerminalPlatformRuntimeMutation(ctx context.Context, state ManagedPlatformOperationStore, op store.ManagedPlatformOperation, kind string, existing runtime.Object, prior map[string]store.PlatformResourceClaim, before func() error, update func(runtime.Object, metav1.UpdateOptions) (runtime.Object, error)) (runtime.Object, error) {
	observed := existing.(metav1.Object)
	key := supabaseClaimKey(kind, observed.GetName())
	repair, err := state.PlatformRuntimeMutationForRepair(ctx, op, key)
	if err != nil || repair.Mutation.Token == "" {
		return existing, err
	}
	pending := repair.Mutation
	claim, ok := prior[key]
	if !ok || claim.PlatformRevision != repair.Claim.PlatformRevision || claim.OwnerOperationID != repair.Claim.OwnerOperationID || claim.ImmutableGeneration != pending.OldGeneration || claim.ResourceID != pending.ResourceID || pending.ResourceID != string(observed.GetUID()) {
		return existing, fmt.Errorf("terminal platform workload mutation ownership changed")
	}
	applied := observed.GetAnnotations()[platformRuntimeTransitionAnnotation] == pending.Token
	if applied {
		digest, err := platformRuntimeSpecDigest(existing)
		if err != nil {
			return existing, err
		}
		if observed.GetGeneration() != pending.NewGeneration || digest != pending.SpecSHA256 {
			return existing, fmt.Errorf("terminal platform workload mutation result changed")
		}
	} else {
		if observed.GetGeneration() != pending.OldGeneration {
			return existing, fmt.Errorf("terminal platform workload changed outside its pending mutation")
		}
		if observed.GetLabels()[platformRuntimeUnappliedLabel] != pending.Token {
			// Workload labels do not advance Kubernetes generation. This CAS
			// invalidates the resourceVersion held by any delayed original write.
			fenced := existing.DeepCopyObject()
			metadata := fenced.(metav1.Object)
			labels := metadata.GetLabels()
			if labels == nil {
				labels = map[string]string{}
			}
			labels[platformRuntimeUnappliedLabel] = pending.Token
			metadata.SetLabels(labels)
			digest, err := platformRuntimeSpecDigest(existing)
			if err != nil {
				return existing, err
			}
			if err = before(); err != nil {
				return existing, err
			}
			result, err := update(fenced, metav1.UpdateOptions{})
			if err != nil {
				return existing, err
			}
			actual := result.(metav1.Object)
			actualDigest, err := platformRuntimeSpecDigest(result)
			if err != nil {
				return existing, err
			}
			if string(actual.GetUID()) != pending.ResourceID || actual.GetGeneration() != pending.OldGeneration || actualDigest != digest || actual.GetLabels()[platformRuntimeUnappliedLabel] != pending.Token || actual.GetAnnotations()[platformRuntimeTransitionAnnotation] == pending.Token {
				return existing, fmt.Errorf("terminal platform workload fence changed its runtime")
			}
			existing = result
		}
	}
	if err = state.ResolvePlatformRuntimeMutationRepair(ctx, op, repair, applied); err != nil {
		return existing, err
	}
	if applied {
		claim.ImmutableGeneration = pending.NewGeneration
	}
	prior[key] = claim
	return existing, nil
}

func (c *Client) repairTerminalPlatformDeletionMutation(ctx context.Context, state ManagedPlatformOperationStore, op store.ManagedPlatformOperation, component string, object metav1.Object, prior map[string]store.PlatformResourceClaim, before func() error) (metav1.Object, error) {
	var kind string
	var update func(runtime.Object, metav1.UpdateOptions) (runtime.Object, error)
	switch value := object.(type) {
	case *appsv1.Deployment:
		kind = "deployment"
		update = func(object runtime.Object, options metav1.UpdateOptions) (runtime.Object, error) {
			return c.kube.AppsV1().Deployments(value.Namespace).Update(ctx, object.(*appsv1.Deployment), options)
		}
	case *appsv1.StatefulSet:
		kind = "statefulset"
		update = func(object runtime.Object, options metav1.UpdateOptions) (runtime.Object, error) {
			return c.kube.AppsV1().StatefulSets(value.Namespace).Update(ctx, object.(*appsv1.StatefulSet), options)
		}
	default:
		return object, nil
	}
	if component != supabaseClaimKey(kind, object.GetName()) {
		return object, fmt.Errorf("terminal platform workload component changed")
	}
	result, err := repairTerminalPlatformRuntimeMutation(ctx, state, op, kind, object.(runtime.Object), prior, before, update)
	if err != nil {
		return object, err
	}
	return result.(metav1.Object), nil
}
