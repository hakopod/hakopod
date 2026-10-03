//go:build hakopod_native_acceptance && linux

package cluster

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/hakopod/hakopod/internal/managedplatform"
	"github.com/hakopod/hakopod/internal/nativeacceptance"
	"github.com/hakopod/hakopod/internal/store"
)

type NeonNativeMigrationObservation struct {
	PlatformID              string   `json:"platform_id"`
	PlatformRevision        int64    `json:"platform_revision"`
	NamespaceUID            string   `json:"namespace_uid"`
	TenantID                string   `json:"tenant_id"`
	TimelineID              string   `json:"timeline_id"`
	SourceNodeID            int64    `json:"source_node_id"`
	DestinationNodeID       int64    `json:"destination_node_id"`
	GenerationBefore        int64    `json:"generation_before"`
	GenerationAfter         int64    `json:"generation_after"`
	TenantOwnerUnchanged    bool     `json:"tenant_owner_unchanged"`
	ControllerMoveCompleted bool     `json:"controller_move_completed"`
	ComputeNames            []string `json:"compute_names"`
	ComputeRoutingVerified  bool     `json:"compute_routing_verified"`
}

func (c *Client) MigrateNeonNative(ctx context.Context, state *store.Store, op store.ManagedPlatformOperation, request NeonRuntimeRequest, key []byte, namespaceUID string, source, destination int64) (NeonNativeMigrationObservation, error) {
	var result NeonNativeMigrationObservation
	if state == nil || namespaceUID == "" || source == destination || source < 1 || destination < 1 || request.Render.Spec.Neon == nil || source > int64(request.Render.Spec.Neon.Pageservers) || destination > int64(request.Render.Spec.Neon.Pageservers) {
		return result, store.ErrInput
	}
	err := state.WithNativeNeonRevision(ctx, op, func() error {
		before, err := c.ProbeNeonNative(ctx, state, op, request, key)
		if err != nil || before.NamespaceUID != namespaceUID || before.AttachedPageserverNodeID != source {
			return fmt.Errorf("native Neon migration source is not the exact owned namespace and attachment")
		}
		platform, _, err := state.ManagedPlatformRecoveryContract(ctx, op.PlatformID, op.Revision)
		if err != nil {
			return err
		}
		if err = nativeacceptance.Recheck(ctx, platform.Project, platform.Environment, "neon"); err != nil {
			return err
		}
		zones, err := c.neonAvailabilityZones(ctx, request.Render.Spec)
		if err != nil {
			return err
		}
		runtime, lifecycle, _, err := prepareNeonNativeProbeRuntime(ctx, request, state, key, zones)
		if err != nil {
			return err
		}
		stored, err := state.ManagedPlatformRecoveryClaims(ctx, op.PlatformID, op.Revision)
		if err != nil {
			return err
		}
		claims := make([]managedplatform.DurableResourceClaim, 0, len(stored))
		for _, claim := range stored {
			claims = append(claims, managedplatform.DurableResourceClaim{PlatformID: claim.PlatformID, PlatformRevision: claim.PlatformRevision, Component: claim.Component, Kind: claim.Kind, ResourceID: claim.ResourceID, ImmutableGeneration: claim.ImmutableGeneration, OwnerOperationID: claim.OwnerOperationID})
		}
		generationBefore, generationAfter, err := runtime.MigrateTenantNative(ctx, op.PlatformID, lifecycle.TenantID, source, destination, claims)
		if err != nil {
			return err
		}
		after, err := c.ProbeNeonNative(ctx, state, op, request, key)
		if err != nil || after.NamespaceUID != namespaceUID || after.TenantID != before.TenantID || after.TimelineID != before.TimelineID || after.AttachedPageserverNodeID != destination || after.TenantGeneration != generationAfter || generationBefore != before.TenantGeneration {
			return fmt.Errorf("native Neon migration did not persist the exact owned destination")
		}
		routing, err := state.NeonControllerState(ctx, op.PlatformID, op.Revision)
		if err != nil {
			return err
		}
		configs := map[string]json.RawMessage{}
		for name, raw := range lifecycle.ComputeConfig {
			configs[name], err = managedplatform.BindNeonControllerRouting(raw, op.PlatformID, lifecycle.TenantID, lifecycle.TimelineID, request.Render.Spec.Neon.Pageservers, routing)
			if err != nil {
				return err
			}
		}
		if err = runtime.VerifyNativeComputeRouting(ctx, managedplatform.NeonNativeProbeRequest{TenantID: lifecycle.TenantID, TimelineID: lifecycle.TimelineID, Claims: claims}, configs); err != nil {
			return err
		}
		result = NeonNativeMigrationObservation{PlatformID: op.PlatformID, PlatformRevision: op.Revision, NamespaceUID: namespaceUID, TenantID: before.TenantID, TimelineID: before.TimelineID, SourceNodeID: source, DestinationNodeID: destination, GenerationBefore: generationBefore, GenerationAfter: generationAfter, TenantOwnerUnchanged: true, ControllerMoveCompleted: true, ComputeNames: after.ComputeNames, ComputeRoutingVerified: true}
		return nil
	})
	return result, err
}
