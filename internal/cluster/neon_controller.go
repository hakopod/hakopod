package cluster

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/hakopod/hakopod/internal/managedplatform"
	"github.com/hakopod/hakopod/internal/platformbackup"
	"github.com/hakopod/hakopod/internal/store"
)

type neonControllerStateReader interface {
	NeonControllerState(context.Context, string, int64) (managedplatform.NeonControllerState, error)
}

type neonControllerStateStore interface {
	WithNeonControllerState(context.Context, string, int64, func(store.NeonControllerContext) (managedplatform.NeonControllerState, bool, error)) (bool, error)
}

type neonRecoveryControllerStateStore interface {
	WithNeonRecoveryControllerState(context.Context, platformbackup.Operation, func(store.NeonControllerContext) (managedplatform.NeonControllerState, bool, error)) (bool, error)
}

func prepareNeonControllerSecret(request *NeonRuntimeRequest, key []byte) error {
	op := request.Operation
	token, err := managedplatform.NeonControllerToken(key, op.PlatformID, op.Revision)
	if err != nil {
		return err
	}
	name := managedplatform.NeonControllerCallbackSecretName(op.Revision)
	if _, exists := request.SecretSnapshots[name]; exists {
		return fmt.Errorf("Neon controller runtime secret name is reserved")
	}
	ref := request.Render.Spec.Secrets["controller-database-password"]
	password := request.SecretSnapshots[ref.Name+"-r"+strconv.FormatInt(ref.Revision, 10)]["value"]
	if len(password) < 1 || len(password) > 8192 {
		return fmt.Errorf("Neon controller database credential is unavailable")
	}
	u := url.URL{Scheme: "postgresql", Host: "neon-controller-database.managed-platform-" + op.PlatformID + ".svc:5432", Path: "/storage_controller", User: url.UserPassword("storage_controller", string(password))}
	u.RawQuery = "sslmode=require"
	request.SecretSnapshots[name] = map[string][]byte{"token": []byte(token), "database-url": []byte(u.String())}
	return nil
}

func (r *ManagedPlatformRuntime) ValidNeonControllerToken(platformID string, revision int64, token string) bool {
	return r != nil && managedplatform.ValidNeonControllerToken(r.EncryptionKey, platformID, revision, token)
}

func (r *ManagedPlatformRuntime) NotifyNeonController(ctx context.Context, database *store.Store, platformID string, revision int64, attach *managedplatform.NeonAttachNotification, safekeepers *managedplatform.NeonSafekeeperNotification) (bool, error) {
	if r == nil || r.Cluster == nil || database == nil || (attach == nil) == (safekeepers == nil) {
		return false, store.ErrInput
	}
	return r.applyNeonController(ctx, database, platformID, revision, attach, safekeepers)
}

func (c *Client) replayNeonComputes(ctx context.Context, state ManagedPlatformOperationStore, encryptionKey []byte, op store.ManagedPlatformOperation) error {
	database, ok := state.(neonControllerStateStore)
	if !ok {
		return fmt.Errorf("Neon compute maintenance requires durable controller state")
	}
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	runtime := &ManagedPlatformRuntime{Cluster: c, EncryptionKey: encryptionKey}
	applied, err := runtime.applyNeonController(ctx, database, op.PlatformID, op.Revision, nil, nil)
	if err != nil {
		return err
	}
	if !applied {
		return fmt.Errorf("Neon compute maintenance is pending")
	}
	return nil
}

func (r *ManagedPlatformRuntime) applyNeonController(ctx context.Context, database neonControllerStateStore, platformID string, revision int64, attach *managedplatform.NeonAttachNotification, safekeepers *managedplatform.NeonSafekeeperNotification) (bool, error) {
	return database.WithNeonControllerState(ctx, platformID, revision, func(input store.NeonControllerContext) (managedplatform.NeonControllerState, bool, error) {
		snapshot, err := OpenManagedPlatformSnapshot(r.EncryptionKey, input.Operation)
		if err != nil || snapshot.Neon == nil {
			return input.State, false, fmt.Errorf("Neon controller snapshot is unavailable")
		}
		request := *snapshot.Neon
		if request.Render.PlatformID != platformID || request.Render.Revision != revision || !reflect.DeepEqual(request.Render.Spec, input.Operation.Spec) || request.Render.Spec.Neon == nil {
			return input.State, false, store.ErrConflict
		}
		tenantID, timelineID := neonDeterministicID(platformID, "tenant"), neonDeterministicID(platformID, "timeline")
		if input.Binding.OperationID != "" {
			if input.Binding.TargetPlatformID != platformID || input.Binding.TargetRevision > revision {
				return input.State, false, store.ErrConflict
			}
			tenantID, timelineID = input.Binding.TenantID, input.Binding.TimelineID
		}
		next, err := mergeNeonControllerState(input.State, tenantID, timelineID, request.Render.Spec.Neon.Pageservers, attach, safekeepers)
		if err != nil {
			return input.State, false, err
		}
		claims := make([]managedplatform.DurableResourceClaim, 0, 6)
		claimed := map[string]store.PlatformResourceClaim{}
		for _, claim := range input.Claims {
			if _, duplicate := claimed[claim.Component]; duplicate {
				return input.State, false, store.ErrConflict
			}
			claimed[claim.Component] = claim
			if strings.HasPrefix(claim.Component, "compute-compute-") {
				claims = append(claims, managedplatform.DurableResourceClaim{PlatformID: claim.PlatformID, PlatformRevision: claim.PlatformRevision, Component: claim.Component, Kind: claim.Kind, ResourceID: claim.ResourceID, ImmutableGeneration: claim.ImmutableGeneration, OwnerOperationID: claim.OwnerOperationID})
			}
		}
		// Tenant creation may synchronously notify before its durable claim.
		// Bootstrap routing unblocks that creation without adopting live state.
		if _, tenantOwned := claimed["tenant"]; !tenantOwned {
			if len(claims) > 0 {
				return input.State, false, store.ErrConflict
			}
			return next, !input.PendingCompute, nil
		}
		if next.Attach == nil {
			return next, false, nil
		}
		if request.Render.Spec.TLSMode == "managed" {
			if err = r.Cluster.readManagedPlatformTLS(ctx, platformID, &request.Render.Spec, &request.Render.PreviousSpec, &request.SecretSnapshots, claimed); err != nil {
				return next, false, nil
			}
		}
		if tenantClaim, exists := claimed["tenant"]; exists {
			nodeID := next.Attach.Shards[0].NodeID
			registration, owned := claimed["pageserver-registration-"+strconv.FormatInt(nodeID-1, 10)]
			if !owned || input.ObserveTenantPlacement == nil {
				return input.State, false, store.ErrConflict
			}
			ref := request.Render.Spec.Secrets["controller-auth"]
			secret := request.SecretSnapshots[ref.Name+"-r"+strconv.FormatInt(ref.Revision, 10)]
			roots := x509.NewCertPool()
			if !roots.AppendCertsFromPEM(secret["ca.crt"]) {
				return input.State, false, fmt.Errorf("Neon controller trust is unavailable")
			}
			toDurable := func(claim store.PlatformResourceClaim) managedplatform.DurableResourceClaim {
				return managedplatform.DurableResourceClaim{PlatformID: claim.PlatformID, PlatformRevision: claim.PlatformRevision, Component: claim.Component, Kind: claim.Kind, ResourceID: claim.ResourceID, ImmutableGeneration: claim.ImmutableGeneration, OwnerOperationID: claim.OwnerOperationID}
			}
			controller := managedplatform.NeonControlTarget{Name: "storage-controller", Origin: "https://neon-storage-controller.managed-platform-" + platformID + ".svc:6699", Token: string(secret["token"])}
			observed, generation, inspectErr := managedplatform.ObserveNeonTenantPlacement(ctx, controller, roots, platformID, tenantID, request.Render.Spec.Neon.Pageservers, nodeID, toDurable(tenantClaim), toDurable(registration))
			if inspectErr != nil {
				return input.State, false, nil
			}
			if err = input.ObserveTenantPlacement(tenantClaim, observed, generation); err != nil {
				return input.State, false, err
			}
		}
		if input.PendingCompute {
			return next, false, nil
		}
		if len(claims) == 0 {
			return next, true, nil
		}
		if next.Safekeepers == nil {
			return next, false, nil
		}
		ref := request.Render.Spec.Secrets["compute-auth"]
		compute := request.SecretSnapshots[ref.Name+"-r"+strconv.FormatInt(ref.Revision, 10)]
		roots := x509.NewCertPool()
		if !roots.AppendCertsFromPEM(compute["ca.crt"]) {
			return next, false, fmt.Errorf("Neon compute trust is unavailable")
		}
		targets := make([]managedplatform.NeonControlTarget, 0, request.Render.Spec.Neon.ComputeReplicas)
		configs := map[string]json.RawMessage{}
		hosts := []string{}
		for i := 0; i < 3; i++ {
			hosts = append(hosts, fmt.Sprintf("neon-safekeeper-%d.managed-platform-%s.svc:5454", i, platformID))
		}
		for i := 0; i < request.Render.Spec.Neon.ComputeReplicas; i++ {
			name := "compute-" + strconv.Itoa(i)
			raw, err := bindNeonComputeConfig(compute["config.json"], tenantID, timelineID, hosts, name)
			if err != nil {
				return next, false, err
			}
			raw, err = managedplatform.BindNeonTenantAuthentication(raw, r.EncryptionKey, platformID, tenantID)
			if err != nil {
				return next, false, err
			}
			configs[name], err = managedplatform.BindNeonControllerRouting(raw, platformID, tenantID, timelineID, request.Render.Spec.Neon.Pageservers, next)
			if err != nil {
				return next, false, err
			}
			targets = append(targets, managedplatform.NeonControlTarget{Name: name, Origin: "https://neon-" + name + "-control.managed-platform-" + platformID + ".svc:3081", Token: string(compute["token"])})
		}
		if err = managedplatform.ApplyNeonComputeNotification(ctx, targets, roots, configs, claims, tenantID, timelineID); err != nil {
			return next, false, nil
		}
		return next, true, nil
	})
}

func mergeNeonControllerState(prior managedplatform.NeonControllerState, tenantID, timelineID string, pageservers int, attach *managedplatform.NeonAttachNotification, safekeepers *managedplatform.NeonSafekeeperNotification) (managedplatform.NeonControllerState, error) {
	next := prior
	// Restore replaces the tenant identity while retaining the target scope.
	// Old routing is never combined with its replacement's notifications.
	if next.Attach != nil && next.Attach.TenantID != tenantID || next.Safekeepers != nil && (next.Safekeepers.TenantID != tenantID || next.Safekeepers.TimelineID != timelineID) {
		next = managedplatform.NeonControllerState{}
	}
	if attach != nil {
		next.Attach = attach
	}
	if safekeepers != nil {
		if next.Safekeepers != nil && safekeepers.Generation < next.Safekeepers.Generation {
			return prior, store.ErrConflict
		}
		next.Safekeepers = safekeepers
	}
	if next.Validate(tenantID, timelineID, pageservers) != nil {
		return prior, store.ErrInput
	}
	return next, nil
}

func neonControllerComputeResolver(reader neonControllerStateReader, platformID string, revision int64, pageservers int) func(context.Context, string, json.RawMessage) (json.RawMessage, error) {
	return func(ctx context.Context, _ string, raw json.RawMessage) (json.RawMessage, error) {
		var value struct {
			Spec struct {
				TenantID   string `json:"tenant_id"`
				TimelineID string `json:"timeline_id"`
			} `json:"spec"`
		}
		if json.Unmarshal(raw, &value) != nil {
			return nil, fmt.Errorf("Neon compute identity is invalid")
		}
		state, err := reader.NeonControllerState(ctx, platformID, revision)
		if err != nil {
			return nil, fmt.Errorf("Neon controller routing has not arrived")
		}
		return managedplatform.BindNeonControllerRouting(raw, platformID, value.Spec.TenantID, value.Spec.TimelineID, pageservers, state)
	}
}

func neonControllerTimelineObserver(database neonControllerStateStore, operation store.ManagedPlatformOperation) func(context.Context, managedplatform.NeonTimelineRoutingObservation) error {
	return func(ctx context.Context, observation managedplatform.NeonTimelineRoutingObservation) error {
		_, err := database.WithNeonControllerState(ctx, operation.PlatformID, operation.Revision, applyNeonTimelineRoutingObservation(operation, observation))
		return err
	}
}

func neonRecoveryTimelineObserver(database neonRecoveryControllerStateStore, recovery platformbackup.Operation, operation store.ManagedPlatformOperation) func(context.Context, managedplatform.NeonTimelineRoutingObservation) error {
	return func(ctx context.Context, observation managedplatform.NeonTimelineRoutingObservation) error {
		_, err := database.WithNeonRecoveryControllerState(ctx, recovery, applyNeonTimelineRoutingObservation(operation, observation))
		return err
	}
}

func applyNeonTimelineRoutingObservation(operation store.ManagedPlatformOperation, observation managedplatform.NeonTimelineRoutingObservation) func(store.NeonControllerContext) (managedplatform.NeonControllerState, bool, error) {
	return func(input store.NeonControllerContext) (managedplatform.NeonControllerState, bool, error) {
		if input.Operation.ID != operation.ID || input.Operation.PlatformID != operation.PlatformID || input.Operation.Revision != operation.Revision || input.Operation.Spec.Neon == nil || observation.Claim.OwnerOperationID != operation.ID {
			return input.State, false, store.ErrConflict
		}
		tenantID, timelineID := neonDeterministicID(operation.PlatformID, "tenant"), neonDeterministicID(operation.PlatformID, "timeline")
		if input.Binding.OperationID != "" {
			if input.Binding.TargetPlatformID != operation.PlatformID || input.Binding.TargetRevision > operation.Revision {
				return input.State, false, store.ErrConflict
			}
			tenantID, timelineID = input.Binding.TenantID, input.Binding.TimelineID
		}
		pageservers := input.Operation.Spec.Neon.Pageservers
		if observation.Validate(operation.PlatformID, operation.Revision, tenantID, timelineID, pageservers) != nil {
			return input.State, false, store.ErrConflict
		}
		matches := 0
		for _, claim := range input.Claims {
			if claim.Component != "timeline" {
				continue
			}
			actual := managedplatform.DurableResourceClaim{PlatformID: claim.PlatformID, PlatformRevision: claim.PlatformRevision, Component: claim.Component, Kind: claim.Kind, ResourceID: claim.ResourceID, ImmutableGeneration: claim.ImmutableGeneration, OwnerOperationID: claim.OwnerOperationID}
			if actual != observation.Claim {
				return input.State, false, store.ErrConflict
			}
			matches++
		}
		if matches != 1 {
			return input.State, false, store.ErrConflict
		}
		next, err := mergeNeonControllerState(input.State, tenantID, timelineID, pageservers, nil, nil)
		if err != nil {
			return input.State, false, err
		}
		if existing := next.Safekeepers; existing != nil {
			// Bootstrap never replaces a callback, including one committed
			// after the provider observation was read.
			if existing.Generation > observation.Routing.Generation {
				return next, true, nil
			}
			if existing.Generation != observation.Routing.Generation || !sameNeonSafekeeperMembers(existing.Safekeepers, observation.Routing.Safekeepers) {
				return input.State, false, store.ErrConflict
			}
			return next, true, nil
		}
		next.Safekeepers = &observation.Routing
		return next, true, nil
	}
}

func sameNeonSafekeeperMembers(left, right []managedplatform.NeonSafekeeperMember) bool {
	if len(left) != len(right) {
		return false
	}
	byID := make(map[int64]*string, len(left))
	for _, member := range left {
		byID[member.ID] = member.Hostname
	}
	for _, member := range right {
		host, ok := byID[member.ID]
		if !ok || host != nil && member.Hostname != nil && *host != *member.Hostname {
			return false
		}
	}
	return true
}
