package store

import (
	"context"

	"github.com/hakopod/hakopod/internal/managedplatform"
)

type managedPlatformLifecycle struct {
	store     *Store
	operation ManagedPlatformOperation
}

// ManagedPlatformLifecycle binds a claimed PostgreSQL operation to the
// dependency-free reconciler contract. The returned adapter remains valid
// only while the operation's exact lease remains current.
func (s *Store) ManagedPlatformLifecycle(operation ManagedPlatformOperation) managedplatform.DurableLifecycle {
	return &managedPlatformLifecycle{store: s, operation: operation}
}

func (l *managedPlatformLifecycle) Operation() managedplatform.DurableOperation {
	return managedplatform.DurableOperation{ID: l.operation.ID, PlatformID: l.operation.PlatformID, Revision: l.operation.Revision, Kind: l.operation.Kind}
}

func (l *managedPlatformLifecycle) Heartbeat(ctx context.Context) error {
	return l.store.HeartbeatManagedPlatformOperation(ctx, l.operation)
}

func durablePlatformClaim(claim PlatformResourceClaim) managedplatform.DurableResourceClaim {
	return managedplatform.DurableResourceClaim{PlatformID: claim.PlatformID, PlatformRevision: claim.PlatformRevision, Component: claim.Component, Kind: claim.Kind, ResourceID: claim.ResourceID, ImmutableGeneration: claim.ImmutableGeneration, OwnerOperationID: claim.OwnerOperationID}
}

func storePlatformClaim(claim managedplatform.DurableResourceClaim) PlatformResourceClaim {
	return PlatformResourceClaim{PlatformID: claim.PlatformID, PlatformRevision: claim.PlatformRevision, Component: claim.Component, Kind: claim.Kind, ResourceID: claim.ResourceID, ImmutableGeneration: claim.ImmutableGeneration, OwnerOperationID: claim.OwnerOperationID}
}

func durablePlatformIntent(intent PlatformResourceIntent) managedplatform.DurableResourceIntent {
	return managedplatform.DurableResourceIntent{ID: intent.ID, PlatformID: intent.PlatformID, PlatformRevision: intent.PlatformRevision, Component: intent.Component, Kind: intent.Kind, ExternalKey: intent.ExternalKey, OwnerOperationID: intent.OwnerOperationID, Confirmed: intent.ConfirmedAt != nil}
}

func storePlatformIntent(intent managedplatform.DurableResourceIntent) PlatformResourceIntent {
	return PlatformResourceIntent{ID: intent.ID, PlatformID: intent.PlatformID, PlatformRevision: intent.PlatformRevision, Component: intent.Component, Kind: intent.Kind, ExternalKey: intent.ExternalKey, OwnerOperationID: intent.OwnerOperationID}
}

func (l *managedPlatformLifecycle) Claims(ctx context.Context, revision int64) ([]managedplatform.DurableResourceClaim, error) {
	claims, err := l.store.PlatformResourceClaims(ctx, l.operation, revision)
	if err != nil {
		return nil, err
	}
	result := make([]managedplatform.DurableResourceClaim, 0, len(claims))
	for _, claim := range claims {
		result = append(result, durablePlatformClaim(claim))
	}
	return result, nil
}

func (l *managedPlatformLifecycle) Reserve(ctx context.Context, intent managedplatform.DurableResourceIntent) (managedplatform.DurableResourceIntent, error) {
	reserved, err := l.store.ReservePlatformResourceIntent(ctx, l.operation, storePlatformIntent(intent))
	if err != nil {
		return managedplatform.DurableResourceIntent{}, err
	}
	return durablePlatformIntent(reserved), nil
}

func (l *managedPlatformLifecycle) Intents(ctx context.Context, revision int64) ([]managedplatform.DurableResourceIntent, error) {
	intents, err := l.store.PlatformResourceIntents(ctx, l.operation, revision)
	if err != nil {
		return nil, err
	}
	result := make([]managedplatform.DurableResourceIntent, 0, len(intents))
	for _, intent := range intents {
		result = append(result, durablePlatformIntent(intent))
	}
	return result, nil
}

func (l *managedPlatformLifecycle) Confirm(ctx context.Context, intent managedplatform.DurableResourceIntent, claim managedplatform.DurableResourceClaim) error {
	return l.store.ConfirmPlatformResourceIntent(ctx, l.operation, storePlatformIntent(intent), storePlatformClaim(claim))
}

func (l *managedPlatformLifecycle) Cancel(ctx context.Context, intent managedplatform.DurableResourceIntent) error {
	return l.store.CancelPlatformResourceIntent(ctx, l.operation, storePlatformIntent(intent))
}

func (l *managedPlatformLifecycle) Claim(ctx context.Context, claim managedplatform.DurableResourceClaim) error {
	return l.store.ClaimPlatformResource(ctx, l.operation, storePlatformClaim(claim))
}

func (l *managedPlatformLifecycle) Advance(ctx context.Context, prior managedplatform.DurableResourceClaim) (managedplatform.DurableResourceClaim, error) {
	if err := l.store.AdvancePlatformResourceClaim(ctx, l.operation, storePlatformClaim(prior)); err != nil {
		return managedplatform.DurableResourceClaim{}, err
	}
	prior.PlatformRevision = l.operation.Revision
	prior.OwnerOperationID = l.operation.ID
	return prior, nil
}

func (l *managedPlatformLifecycle) Verify(ctx context.Context, claim managedplatform.DurableResourceClaim) error {
	return l.store.VerifyPlatformResourceClaim(ctx, l.operation, storePlatformClaim(claim))
}

func (l *managedPlatformLifecycle) Release(ctx context.Context, claim managedplatform.DurableResourceClaim) error {
	return l.store.ReleasePlatformResourceClaim(ctx, l.operation, storePlatformClaim(claim))
}
