//go:build hakopod_native_acceptance && linux

package cluster

import (
	"context"
	"fmt"
	"reflect"

	"github.com/hakopod/hakopod/internal/managedplatform"
	"github.com/hakopod/hakopod/internal/nativeacceptance"
	"github.com/hakopod/hakopod/internal/store"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type NeonNativeProbeObservation struct {
	managedplatform.NeonNativeProbeResult
	PlatformID       string                   `json:"platform_id"`
	PlatformRevision int64                    `json:"platform_revision"`
	NamespaceUID     string                   `json:"namespace_uid"`
	TLS              neonNativeTLSObservation `json:"tls"`
}

// ProbeNeonNative rechecks the development-only native gate and the exact
// accepted platform snapshot on both sides of read-only provider inspection.
func (c *Client) ProbeNeonNative(ctx context.Context, state *store.Store, op store.ManagedPlatformOperation, request NeonRuntimeRequest, encryptionKey []byte) (NeonNativeProbeObservation, error) {
	var observation NeonNativeProbeObservation
	if c == nil || c.kube == nil || state == nil || op.ID == "" || op.PlatformID == "" || op.Revision < 1 || op.Status != "succeeded" || op.Kind != "create" && op.Kind != "update" || request.Operation.ID != op.ID || request.Operation.PlatformID != op.PlatformID || request.Operation.Revision != op.Revision || request.Render.PlatformID != op.PlatformID || request.Render.Revision != op.Revision || request.Render.Spec.Kind != "neon" || !reflect.DeepEqual(request.Render.Spec, op.Spec) {
		return observation, fmt.Errorf("native Neon probe requires the exact accepted operation snapshot")
	}
	acceptedSnapshot, err := state.ManagedPlatformRecoverySnapshot(ctx, op.PlatformID, op.Revision)
	if err != nil || acceptedSnapshot.Status != "succeeded" || !reflect.DeepEqual(acceptedSnapshot, op) {
		return observation, fmt.Errorf("native Neon probe operation snapshot changed")
	}
	platform, _, err := state.ManagedPlatformRecoveryContract(ctx, op.PlatformID, op.Revision)
	if err != nil || platform.ID != op.PlatformID || platform.Project == "" || platform.Environment == "" || platform.Revision != op.Revision || platform.Status != "ready" || platform.DeletedAt != nil || platform.Spec.Kind != "neon" || !reflect.DeepEqual(platform.Spec, op.Spec) {
		return observation, fmt.Errorf("native Neon probe platform revision changed")
	}
	if err = nativeacceptance.Recheck(ctx, platform.Project, platform.Environment, "neon"); err != nil {
		return observation, err
	}
	claimsByComponent, err := state.ManagedPlatformRecoveryClaims(ctx, op.PlatformID, op.Revision)
	if err != nil {
		return observation, fmt.Errorf("load native Neon accepted claims: %w", err)
	}
	ns, err := c.kube.CoreV1().Namespaces().Get(ctx, "managed-platform-"+op.PlatformID, metav1.GetOptions{})
	if err != nil || ns.DeletionTimestamp != nil || ns.Labels[managedBy] != "hakopod" || ns.Labels["hakopod.io/managed-platform-id"] != op.PlatformID {
		return observation, fmt.Errorf("native Neon owned namespace is unavailable")
	}
	if err = verifySupabaseClaimedUID("namespace", ns, claimsByComponent); err != nil {
		return observation, fmt.Errorf("native Neon namespace ownership changed")
	}
	zones, err := c.neonAvailabilityZones(ctx, request.Render.Spec)
	if err != nil {
		return observation, err
	}
	runtime, runtimeRequest, _, err := prepareNeonNativeProbeRuntime(ctx, request, state, encryptionKey, zones)
	if err != nil {
		return observation, err
	}
	claims := make([]managedplatform.DurableResourceClaim, 0, len(claimsByComponent))
	for _, claim := range claimsByComponent {
		if claim.PlatformID != op.PlatformID || claim.PlatformRevision != op.Revision || claim.OwnerOperationID != op.ID {
			return observation, fmt.Errorf("native Neon accepted ownership claim is invalid")
		}
		claims = append(claims, managedplatform.DurableResourceClaim{PlatformID: claim.PlatformID, PlatformRevision: claim.PlatformRevision, Component: claim.Component, Kind: claim.Kind, ResourceID: claim.ResourceID, ImmutableGeneration: claim.ImmutableGeneration, OwnerOperationID: claim.OwnerOperationID})
	}
	result, err := runtime.ProbeNeonNative(ctx, managedplatform.NeonNativeProbeRequest{TenantID: runtimeRequest.TenantID, TimelineID: runtimeRequest.TimelineID, Claims: claims})
	if err != nil {
		return observation, err
	}
	tlsObservation, err := probeNeonNativeTLS(ctx, request)
	if err != nil {
		return observation, err
	}
	acceptedSnapshotAfter, err := state.ManagedPlatformRecoverySnapshot(ctx, op.PlatformID, op.Revision)
	if err != nil || acceptedSnapshotAfter.Status != "succeeded" || !reflect.DeepEqual(acceptedSnapshotAfter, acceptedSnapshot) {
		return observation, fmt.Errorf("native Neon probe operation snapshot changed during probe")
	}
	claimsAfter, err := state.ManagedPlatformRecoveryClaims(ctx, op.PlatformID, op.Revision)
	if err != nil || !reflect.DeepEqual(claimsAfter, claimsByComponent) {
		return observation, fmt.Errorf("native Neon accepted ownership claims changed during probe")
	}
	platformAfter, _, err := state.ManagedPlatformRecoveryContract(ctx, op.PlatformID, op.Revision)
	if err != nil || platformAfter.ID != platform.ID || platformAfter.Project != platform.Project || platformAfter.Environment != platform.Environment || platformAfter.Revision != platform.Revision || platformAfter.Status != "ready" || platformAfter.DeletedAt != nil || !reflect.DeepEqual(platformAfter.Spec, platform.Spec) {
		return observation, fmt.Errorf("native Neon platform changed during probe")
	}
	if err = nativeacceptance.Recheck(ctx, platform.Project, platform.Environment, "neon"); err != nil {
		return observation, err
	}
	nsAfter, err := c.kube.CoreV1().Namespaces().Get(ctx, ns.Name, metav1.GetOptions{})
	if err != nil || nsAfter.UID != ns.UID || nsAfter.DeletionTimestamp != nil || nsAfter.Labels[managedBy] != "hakopod" || nsAfter.Labels["hakopod.io/managed-platform-id"] != op.PlatformID {
		return observation, fmt.Errorf("native Neon namespace changed during probe")
	}
	observation = NeonNativeProbeObservation{NeonNativeProbeResult: result, PlatformID: op.PlatformID, PlatformRevision: op.Revision, NamespaceUID: string(ns.UID), TLS: tlsObservation}
	return observation, nil
}

type neonNativeProbeBindingReader interface {
	NeonRecoveryBindingForTarget(context.Context, string, int64) (store.NeonRecoveryBinding, error)
}

// Native probes inspect completed revisions without acquiring mutation leases.
// The surrounding probe rechecks the accepted snapshot and claims after use.
type neonNativeProbeBindings struct {
	reader    neonNativeProbeBindingReader
	operation store.ManagedPlatformOperation
}

func (b neonNativeProbeBindings) NeonRecoveryBindingForLifecycle(ctx context.Context, op store.ManagedPlatformOperation) (store.NeonRecoveryBinding, error) {
	if b.reader == nil || op.ID == "" || op.PlatformID == "" || op.Revision < 1 || op.Status != "succeeded" || op.Kind != "create" && op.Kind != "update" || !reflect.DeepEqual(op, b.operation) {
		return store.NeonRecoveryBinding{}, store.ErrConflict
	}
	binding, err := b.reader.NeonRecoveryBindingForTarget(ctx, op.PlatformID, op.Revision)
	if err != nil {
		return store.NeonRecoveryBinding{}, err
	}
	if binding.TargetPlatformID != op.PlatformID || binding.TargetRevision < 1 || binding.TargetRevision > op.Revision {
		return store.NeonRecoveryBinding{}, store.ErrConflict
	}
	return binding, nil
}

func prepareNeonNativeProbeRuntime(ctx context.Context, request NeonRuntimeRequest, reader neonNativeProbeBindingReader, encryptionKey []byte, zones []string) (*managedplatform.DurableNeonRuntime, managedplatform.NeonLifecycleRequest, managedplatform.NeonProxyEndpointState, error) {
	op := request.Operation
	accepted := managedplatform.DurableOperation{ID: op.ID, PlatformID: op.PlatformID, Revision: op.Revision, Kind: op.Kind}
	bindings := neonNativeProbeBindings{reader: reader, operation: op}
	return prepareNeonLifecycleWithAdapter(ctx, request, neonNativeProbeLifecycle{operation: accepted}, bindings, encryptionKey, zones)
}

// The constructor is allowed to read accepted snapshot bindings. Every
// lifecycle method is a hard failure, keeping the probe outside mutation and
// lease authority.
type neonNativeProbeLifecycle struct {
	operation managedplatform.DurableOperation
}

func (l neonNativeProbeLifecycle) Operation() managedplatform.DurableOperation {
	return l.operation
}
func neonNativeProbeLifecycleError() error {
	return fmt.Errorf("native Neon probe cannot access lifecycle authority")
}
func (neonNativeProbeLifecycle) Heartbeat(context.Context) error {
	return neonNativeProbeLifecycleError()
}
func (neonNativeProbeLifecycle) Claims(context.Context, int64) ([]managedplatform.DurableResourceClaim, error) {
	return nil, neonNativeProbeLifecycleError()
}
func (neonNativeProbeLifecycle) Reserve(context.Context, managedplatform.DurableResourceIntent) (managedplatform.DurableResourceIntent, error) {
	return managedplatform.DurableResourceIntent{}, neonNativeProbeLifecycleError()
}
func (neonNativeProbeLifecycle) Intents(context.Context, int64) ([]managedplatform.DurableResourceIntent, error) {
	return nil, neonNativeProbeLifecycleError()
}
func (neonNativeProbeLifecycle) Confirm(context.Context, managedplatform.DurableResourceIntent, managedplatform.DurableResourceClaim) error {
	return neonNativeProbeLifecycleError()
}
func (neonNativeProbeLifecycle) Cancel(context.Context, managedplatform.DurableResourceIntent) error {
	return neonNativeProbeLifecycleError()
}
func (neonNativeProbeLifecycle) Claim(context.Context, managedplatform.DurableResourceClaim) error {
	return neonNativeProbeLifecycleError()
}
func (neonNativeProbeLifecycle) Advance(context.Context, managedplatform.DurableResourceClaim) (managedplatform.DurableResourceClaim, error) {
	return managedplatform.DurableResourceClaim{}, neonNativeProbeLifecycleError()
}
func (neonNativeProbeLifecycle) Verify(context.Context, managedplatform.DurableResourceClaim) error {
	return neonNativeProbeLifecycleError()
}
func (neonNativeProbeLifecycle) Release(context.Context, managedplatform.DurableResourceClaim) error {
	return neonNativeProbeLifecycleError()
}
