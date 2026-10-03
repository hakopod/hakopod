package cluster

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"

	"github.com/hakopod/hakopod/internal/store"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

const platformRuntimeTransitionAnnotation = "hakopod.io/runtime-transition"

func platformRuntimeSpecDigest(object runtime.Object) (string, error) {
	var spec any
	switch value := object.(type) {
	case *appsv1.Deployment:
		spec = value.Spec
	case *appsv1.StatefulSet:
		spec = value.Spec
	default:
		return "", fmt.Errorf("unsupported platform workload")
	}
	encoded, err := json.Marshal(spec)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func platformRuntimeGeneration(kind string, object metav1.Object) int64 {
	if kind == "deployment" || kind == "statefulset" {
		return object.GetGeneration()
	}
	return 1
}

// Server-side dry run captures Kubernetes defaults before journaling the exact
// desired spec. A retry only advances ownership for that spec, UID and token.
func applyPlatformRuntimeMutation(ctx context.Context, state ManagedPlatformOperationStore, op store.ManagedPlatformOperation, kind string, existing, desired runtime.Object, prior, current map[string]store.PlatformResourceClaim, before func() error, update func(runtime.Object, metav1.UpdateOptions) (runtime.Object, error)) error {
	observed := existing.(metav1.Object)
	key := supabaseClaimKey(kind, observed.GetName())
	pending, err := state.PlatformRuntimeMutation(ctx, op, key)
	if err != nil {
		return err
	}
	if pending.Token != "" {
		claim, ok := current[key]
		if !ok || pending.ResourceID != string(observed.GetUID()) || claim.ResourceID != pending.ResourceID || claim.ImmutableGeneration != pending.OldGeneration {
			return fmt.Errorf("platform workload mutation ownership changed")
		}
		if observed.GetAnnotations()[platformRuntimeTransitionAnnotation] == pending.Token {
			digest, err := platformRuntimeSpecDigest(existing)
			if err != nil {
				return err
			}
			if observed.GetGeneration() != pending.NewGeneration || digest != pending.SpecSHA256 {
				return fmt.Errorf("platform workload mutation result changed")
			}
			if err = state.CompletePlatformRuntimeMutation(ctx, op, claim, pending); err != nil {
				return err
			}
			claim.ImmutableGeneration = pending.NewGeneration
			current[key] = claim
			pending = store.PlatformRuntimeMutation{}
		} else if observed.GetGeneration() != pending.OldGeneration {
			return fmt.Errorf("platform workload changed outside its pending mutation")
		}
	}
	if err = claimOrAdvanceSupabaseObject(ctx, state, op, kind, observed, prior, current, false); err != nil {
		return err
	}
	wanted := desired.(metav1.Object)
	// Keep API-owned metadata and overlay renderer-owned values. The pod spec is
	// still replaced in full; resourceVersion prevents concurrent writes.
	labels := map[string]string{}
	for key, value := range observed.GetLabels() {
		labels[key] = value
	}
	for key, value := range wanted.GetLabels() {
		labels[key] = value
	}
	annotations := map[string]string{}
	for key, value := range observed.GetAnnotations() {
		annotations[key] = value
	}
	for key, value := range wanted.GetAnnotations() {
		annotations[key] = value
	}
	metadataChanged := !reflect.DeepEqual(labels, observed.GetLabels())
	for key, value := range annotations {
		if observed.GetAnnotations()[key] != value {
			metadataChanged = true
		}
	}
	token := pending.Token
	if token == "" {
		token = store.NewID()
	}
	annotations[platformRuntimeTransitionAnnotation] = token
	wanted.SetLabels(labels)
	wanted.SetAnnotations(annotations)
	wanted.SetUID(observed.GetUID())
	wanted.SetResourceVersion(observed.GetResourceVersion())
	if err = before(); err != nil {
		return err
	}
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
	if pending.Token == "" && digest == oldDigest && !metadataChanged {
		return nil
	}
	claim := current[key]
	mutation := store.PlatformRuntimeMutation{Component: key, ResourceID: claim.ResourceID, OldGeneration: claim.ImmutableGeneration, NewGeneration: normalized.(metav1.Object).GetGeneration(), Token: token, SpecSHA256: digest}
	if pending.Token != "" && pending != mutation {
		return fmt.Errorf("platform workload pending mutation differs from its reviewed runtime")
	}
	if err = state.PreparePlatformRuntimeMutation(ctx, op, claim, mutation); err != nil {
		return err
	}
	if err = before(); err != nil {
		return err
	}
	updated, err := update(desired, metav1.UpdateOptions{})
	if err != nil {
		return err
	}
	actual := updated.(metav1.Object)
	actualDigest, err := platformRuntimeSpecDigest(updated)
	if err != nil {
		return err
	}
	if string(actual.GetUID()) != mutation.ResourceID || actual.GetGeneration() != mutation.NewGeneration || actual.GetAnnotations()[platformRuntimeTransitionAnnotation] != mutation.Token || actualDigest != mutation.SpecSHA256 {
		return fmt.Errorf("platform workload write differs from its durable intent")
	}
	if err = state.CompletePlatformRuntimeMutation(ctx, op, claim, mutation); err != nil {
		return err
	}
	claim.ImmutableGeneration = mutation.NewGeneration
	current[key] = claim
	return nil
}
