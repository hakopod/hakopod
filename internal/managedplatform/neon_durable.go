package managedplatform

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/util/validation"
)

const maxDurableNeonResources = MaxComponents * 5

// DurableNeonRuntime is the production persistence shape for Neon control
// operations. PostgreSQL intents distinguish an attempted create from
// provider-confirmed ownership.
type DurableNeonRuntime struct {
	control   *NeonRuntime
	lifecycle DurableLifecycle
}

// Recovery uses the same durable controller routing as ordinary provisioning.
func (r *DurableNeonRuntime) SetComputeConfigResolver(resolve func(context.Context, string, json.RawMessage) (json.RawMessage, error)) {
	r.control.config.ResolveComputeConfig = resolve
}

func NewDurableNeonRuntime(config NeonRuntimeConfig, lifecycle DurableLifecycle) (*DurableNeonRuntime, error) {
	if lifecycle == nil {
		return nil, fmt.Errorf("durable Neon runtime requires PostgreSQL lifecycle state")
	}
	if config.TestOnlyFileState {
		return nil, fmt.Errorf("durable Neon runtime cannot use test-only file state")
	}
	if config.DeprovisionOnly && lifecycle.Operation().Kind != "delete" {
		return nil, fmt.Errorf("Neon deprovision-only runtime requires a delete operation")
	}
	if err := validateNeonTarget(config.StorageController, "storage controller"); err != nil {
		return nil, err
	}
	if len(config.Computes) == 0 || len(config.Computes) > maxNeonComputeNodes {
		return nil, fmt.Errorf("Neon runtime requires 1-%d compute targets", maxNeonComputeNodes)
	}
	seen := map[string]bool{}
	for _, target := range config.Computes {
		if err := validateNeonTarget(target, "compute"); err != nil {
			return nil, err
		}
		if seen[target.Name] {
			return nil, fmt.Errorf("duplicate Neon compute target %q", target.Name)
		}
		seen[target.Name] = true
	}
	if !config.allowMissingStorageRegistrationsForTest && (len(config.Pageservers) < 2 || len(config.Pageservers) > 8 || len(config.Safekeepers) != 3) {
		return nil, fmt.Errorf("Neon runtime requires 2-8 pageserver registrations and exactly three safekeeper registrations")
	}
	pageserverIDs := map[int64]bool{}
	for _, node := range config.Pageservers {
		var err error
		if config.DeprovisionOnly {
			err = validateNeonStorageEndpoint(node.Name, node.NodeID, node.Generation, node.Host)
		} else {
			err = validateNeonStorageNode(node.Name, node.NodeID, node.Generation, node.Host, node.AvailabilityZone)
		}
		if err != nil {
			return nil, fmt.Errorf("pageserver registration: %w", err)
		}
		if pageserverIDs[node.NodeID] {
			return nil, fmt.Errorf("duplicate Neon pageserver node ID")
		}
		pageserverIDs[node.NodeID] = true
	}
	safekeeperIDs := map[int64]bool{}
	safekeeperHosts := map[string]bool{}
	for _, node := range config.Safekeepers {
		var err error
		if config.DeprovisionOnly {
			err = validateNeonStorageEndpoint(node.Name, node.NodeID, node.Generation, node.Host)
		} else {
			err = validateNeonStorageNode(node.Name, node.NodeID, node.Generation, node.Host, node.AvailabilityZone)
		}
		if err != nil {
			return nil, fmt.Errorf("safekeeper registration: %w", err)
		}
		if safekeeperIDs[node.NodeID] || safekeeperHosts[node.Host] {
			return nil, fmt.Errorf("duplicate Neon safekeeper identity")
		}
		safekeeperIDs[node.NodeID] = true
		safekeeperHosts[node.Host] = true
	}
	if len(config.Safekeepers) > 0 && (len(config.SafekeeperToken) < 16 || len(config.SafekeeperToken) > 8192 || strings.ContainsAny(config.SafekeeperToken, "\r\n")) {
		return nil, fmt.Errorf("Neon safekeeper inspection requires a bounded bearer token")
	}
	if config.RequestTimeout == 0 {
		config.RequestTimeout = 20 * time.Second
	}
	if config.RequestTimeout < time.Second || config.RequestTimeout > 2*time.Minute {
		return nil, fmt.Errorf("Neon request timeout must be between one second and two minutes")
	}
	if config.RootCAs == nil {
		return nil, fmt.Errorf("Neon runtime requires an explicit control-plane CA pool")
	}
	transport := &http.Transport{Proxy: nil, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: config.RootCAs}, MaxIdleConns: 8, MaxIdleConnsPerHost: 2, MaxConnsPerHost: 4, IdleConnTimeout: 30 * time.Second, TLSHandshakeTimeout: 5 * time.Second, ResponseHeaderTimeout: config.RequestTimeout, MaxResponseHeaderBytes: 32 << 10}
	client := &http.Client{Transport: transport, Timeout: config.RequestTimeout, CheckRedirect: func(*http.Request, []*http.Request) error {
		return fmt.Errorf("Neon control redirects are not permitted")
	}}
	return &DurableNeonRuntime{control: &NeonRuntime{config: config, client: client}, lifecycle: lifecycle}, nil
}

func (r *DurableNeonRuntime) claims(ctx context.Context) (map[string]DurableResourceClaim, map[string]DurableResourceClaim, error) {
	op := r.lifecycle.Operation()
	prior := map[string]DurableResourceClaim{}
	current := map[string]DurableResourceClaim{}
	load := func(revision int64, destination map[string]DurableResourceClaim, currentRevision bool) error {
		claims, err := r.lifecycle.Claims(ctx, revision)
		if err != nil {
			return err
		}
		if len(claims) > maxDurableNeonResources {
			return fmt.Errorf("Neon ownership inventory exceeds its bound")
		}
		for _, claim := range claims {
			if claim.PlatformID != op.PlatformID || claim.PlatformRevision != revision || claim.Component == "" || claim.ResourceID == "" || claim.ImmutableGeneration < 1 {
				return fmt.Errorf("invalid Neon ownership claim")
			}
			if currentRevision && claim.OwnerOperationID != op.ID {
				return fmt.Errorf("current Neon ownership belongs to another operation")
			}
			// Kubernetes object claims use kind.name keys and are reconciled by the
			// shared cluster lifecycle. This runtime owns only provider identities.
			if strings.Contains(claim.Component, ".") {
				continue
			}
			if _, exists := destination[claim.Component]; exists {
				return fmt.Errorf("duplicate Neon ownership claim")
			}
			destination[claim.Component] = claim
		}
		return nil
	}
	if op.Revision > 1 {
		if err := load(op.Revision-1, prior, false); err != nil {
			return nil, nil, err
		}
	}
	if err := load(op.Revision, current, true); err != nil {
		return nil, nil, err
	}
	return prior, current, nil
}

func (r *DurableNeonRuntime) pendingIntents(ctx context.Context) (map[string]DurableResourceIntent, error) {
	op := r.lifecycle.Operation()
	items, err := r.lifecycle.Intents(ctx, op.Revision)
	if err != nil {
		return nil, err
	}
	if len(items) > maxDurableNeonResources {
		return nil, fmt.Errorf("Neon resource intent inventory exceeds its bound")
	}
	pending := make(map[string]DurableResourceIntent, len(items))
	for _, intent := range items {
		if intent.ID == "" || intent.PlatformID != op.PlatformID || intent.PlatformRevision != op.Revision || intent.Component == "" || intent.Kind == "" || intent.ExternalKey == "" || intent.OwnerOperationID != op.ID {
			return nil, fmt.Errorf("invalid Neon resource intent")
		}
		if intent.Confirmed {
			continue
		}
		if _, exists := pending[intent.Component]; exists {
			return nil, fmt.Errorf("duplicate Neon resource intent")
		}
		pending[intent.Component] = intent
	}
	return pending, nil
}

func pendingIntentFor(pending map[string]DurableResourceIntent, component, kind, externalKey string) (DurableResourceIntent, bool, error) {
	intent, ok := pending[component]
	if !ok {
		return DurableResourceIntent{}, false, nil
	}
	if intent.Component != component || intent.Kind != kind || intent.ExternalKey != externalKey || intent.Confirmed {
		return DurableResourceIntent{}, false, fmt.Errorf("Neon resource intent changed for %s", component)
	}
	return intent, true, nil
}

func (r *DurableNeonRuntime) reserveOrReuse(ctx context.Context, pending map[string]DurableResourceIntent, component, kind, externalKey string) (DurableResourceIntent, error) {
	if intent, ok, err := pendingIntentFor(pending, component, kind, externalKey); err != nil {
		return DurableResourceIntent{}, err
	} else if ok {
		return intent, nil
	}
	intent, err := r.reserve(ctx, component, kind, externalKey)
	if err != nil {
		return DurableResourceIntent{}, err
	}
	pending[component] = intent
	return intent, nil
}

func (r *DurableNeonRuntime) confirmPending(ctx context.Context, pending map[string]DurableResourceIntent, intent DurableResourceIntent, resourceID string, generation int64) (DurableResourceClaim, error) {
	claim, err := r.confirm(ctx, intent, resourceID, generation)
	if err != nil {
		return DurableResourceClaim{}, err
	}
	delete(pending, intent.Component)
	return claim, nil
}

func claimFor(prior, current map[string]DurableResourceClaim, component, kind string) (DurableResourceClaim, bool, bool, error) {
	priorClaim, wasPrior := prior[component]
	currentClaim, isCurrent := current[component]
	if wasPrior && isCurrent {
		return DurableResourceClaim{}, false, false, fmt.Errorf("duplicate Neon ownership across revisions for %s", component)
	}
	claim := currentClaim
	if wasPrior {
		claim = priorClaim
	}
	if wasPrior || isCurrent {
		if claim.Kind != kind {
			return DurableResourceClaim{}, false, false, fmt.Errorf("Neon %s ownership kind changed", component)
		}
		return claim, wasPrior, true, nil
	}
	return DurableResourceClaim{}, false, false, nil
}

func (r *DurableNeonRuntime) activateClaim(ctx context.Context, prior, current map[string]DurableResourceClaim, claim DurableResourceClaim, wasPrior bool) (DurableResourceClaim, error) {
	if wasPrior {
		advanced, err := r.lifecycle.Advance(ctx, claim)
		if err != nil {
			return DurableResourceClaim{}, err
		}
		claim = advanced
		delete(prior, claim.Component)
		current[claim.Component] = claim
	}
	if err := r.lifecycle.Verify(ctx, claim); err != nil {
		return DurableResourceClaim{}, err
	}
	return claim, nil
}

func (r *DurableNeonRuntime) reserve(ctx context.Context, component, kind, externalKey string) (DurableResourceIntent, error) {
	op := r.lifecycle.Operation()
	requested := DurableResourceIntent{PlatformID: op.PlatformID, PlatformRevision: op.Revision, Component: component, Kind: kind, ExternalKey: externalKey, OwnerOperationID: op.ID}
	intent, err := r.lifecycle.Reserve(ctx, requested)
	if err != nil {
		return DurableResourceIntent{}, err
	}
	if intent.ID == "" || intent.PlatformID != requested.PlatformID || intent.PlatformRevision != requested.PlatformRevision || intent.Component != requested.Component || intent.Kind != requested.Kind || intent.ExternalKey != requested.ExternalKey || intent.OwnerOperationID != requested.OwnerOperationID || intent.Confirmed {
		return DurableResourceIntent{}, fmt.Errorf("Neon resource intent does not match the requested create")
	}
	return intent, nil
}

func (r *DurableNeonRuntime) confirm(ctx context.Context, intent DurableResourceIntent, resourceID string, generation int64) (DurableResourceClaim, error) {
	op := r.lifecycle.Operation()
	claim := DurableResourceClaim{PlatformID: op.PlatformID, PlatformRevision: op.Revision, Component: intent.Component, Kind: intent.Kind, ResourceID: resourceID, ImmutableGeneration: generation, OwnerOperationID: op.ID}
	if err := r.lifecycle.Confirm(ctx, intent, claim); err != nil {
		return DurableResourceClaim{}, err
	}
	if err := r.lifecycle.Verify(ctx, claim); err != nil {
		return DurableResourceClaim{}, err
	}
	return claim, nil
}

func (r *DurableNeonRuntime) Provision(ctx context.Context, request NeonLifecycleRequest) (NeonLifecycleState, error) {
	var state NeonLifecycleState
	op := r.lifecycle.Operation()
	if r.control.config.DeprovisionOnly {
		return state, fmt.Errorf("Neon deprovision-only runtime cannot provision resources")
	}
	if (op.Kind != "create" && op.Kind != "update") || request.OperationID != op.ID {
		return state, fmt.Errorf("Neon lifecycle request does not match the leased operation")
	}
	digest, err := validateNeonLifecycleRequest(request, r.control.config.Computes)
	if err != nil {
		return state, err
	}
	// This authenticated preflight precedes lifecycle reads and every provider
	// mutation. Partial protocol support must never expose an adoption window.
	if err = r.control.verifyOwnershipCapability(ctx); err != nil {
		return state, err
	}
	ownershipRequired := !r.control.config.allowUnqualifiedOwnershipProtocolForTest
	state = NeonLifecycleState{SchemaVersion: 2, OperationID: op.ID, RequestDigest: digest, TenantID: request.TenantID, TimelineID: request.TimelineID}
	prior, current, err := r.claims(ctx)
	if err != nil {
		return state, err
	}
	pending, err := r.pendingIntents(ctx)
	if err != nil {
		return state, err
	}
	if !request.CreateTenant {
		return state, fmt.Errorf("durable Neon branching requires an existing Hakopod tenant claim; external tenant adoption is unsupported")
	}
	if err = r.registerStorageNodes(ctx, prior, current, pending); err != nil {
		return state, err
	}

	tenantClaim, tenantWasPrior, tenantOwned, err := claimFor(prior, current, "tenant", "neon_tenant")
	if err != nil {
		return state, err
	}
	claimedTenantIdentity, tenantOwnershipToken := "", ""
	if tenantOwned && ownershipRequired {
		claimedTenantIdentity, tenantOwnershipToken, err = parseNeonOwnedResourceID(tenantClaim.ResourceID)
		if err != nil {
			return state, fmt.Errorf("Neon tenant ownership claim is invalid: %w", err)
		}
	}
	if !tenantOwned && ownershipRequired {
		intent, pendingCreate, intentErr := pendingIntentFor(pending, "tenant", "neon_tenant", request.TenantID)
		if intentErr != nil {
			return state, intentErr
		}
		if pendingCreate {
			// The provider may have persisted the tenant before completing its
			// ownership record. Replay the exact intent through its token and
			// request-hash fence before inspecting the canonical attachment.
			if err = r.lifecycle.Heartbeat(ctx); err != nil {
				return state, err
			}
			response, replayErr := r.control.doJSONOwned(ctx, r.control.config.StorageController, http.MethodPost, "/v1/tenant", neonTenantCreateBody(request), intent.ID, http.StatusCreated)
			if replayErr != nil {
				return state, fmt.Errorf("resume Neon tenant left a pending intent: %w", replayErr)
			}
			if err = verifyNeonOwnershipToken(response, intent.ID); err != nil {
				return state, fmt.Errorf("resume Neon tenant returned invalid ownership: %w", err)
			}
		}
	}
	tenantExists, tenantIdentity, tenantGeneration, observedTenantToken, _, _, err := r.inspectTenant(ctx, request.TenantID)
	if err != nil {
		return state, err
	}
	if tenantExists {
		if !tenantOwned {
			intent, pendingCreate, intentErr := pendingIntentFor(pending, "tenant", "neon_tenant", request.TenantID)
			if intentErr != nil {
				return state, intentErr
			}
			if !pendingCreate {
				return state, fmt.Errorf("Neon tenant exists without a confirmed durable ownership claim or matching pending intent")
			}
			if ownershipRequired && observedTenantToken != intent.ID {
				return state, fmt.Errorf("Neon tenant exists under a different ownership token")
			}
			resourceID := tenantIdentity
			if ownershipRequired {
				resourceID, err = encodeNeonOwnedResourceID(tenantIdentity, intent.ID)
				if err != nil {
					return state, err
				}
			}
			tenantClaim, err = r.confirmPending(ctx, pending, intent, resourceID, tenantGeneration)
			if err != nil {
				return state, err
			}
			current[tenantClaim.Component] = tenantClaim
			tenantOwned = true
			claimedTenantIdentity, tenantOwnershipToken = tenantIdentity, intent.ID
		}
		if ownershipRequired && (claimedTenantIdentity != tenantIdentity || tenantOwnershipToken != observedTenantToken) || !ownershipRequired && tenantClaim.ResourceID != tenantIdentity || tenantClaim.ImmutableGeneration != tenantGeneration {
			return state, fmt.Errorf("Neon tenant attachment identity changed")
		}
		tenantClaim, err = r.activateClaim(ctx, prior, current, tenantClaim, tenantWasPrior)
		if err != nil {
			return state, err
		}
		current[tenantClaim.Component] = tenantClaim
	} else {
		if tenantOwned {
			return state, fmt.Errorf("confirmed Neon tenant disappeared")
		}
		intent, reserveErr := r.reserveOrReuse(ctx, pending, "tenant", "neon_tenant", request.TenantID)
		if reserveErr != nil {
			return state, reserveErr
		}
		if err = r.lifecycle.Heartbeat(ctx); err != nil {
			return state, err
		}
		body := neonTenantCreateBody(request)
		var response []byte
		var createErr error
		if ownershipRequired {
			response, createErr = r.control.doJSONOwned(ctx, r.control.config.StorageController, http.MethodPost, "/v1/tenant", body, intent.ID, http.StatusCreated)
		} else {
			response, createErr = r.control.doJSON(ctx, r.control.config.StorageController, http.MethodPost, "/v1/tenant", body, http.StatusCreated)
		}
		if createErr != nil {
			return state, fmt.Errorf("create Neon tenant left a pending intent: %w", createErr)
		}
		if ownershipRequired {
			if err = verifyNeonOwnershipToken(response, intent.ID); err != nil {
				return state, fmt.Errorf("create Neon tenant returned invalid ownership: %w", err)
			}
		}
		if ownershipRequired {
			// Create responses may retain generation zero from before the
			// provider reconciled its attachment. Confirm only the current
			// completed ownership record and canonical attachment instead.
			var exists bool
			var token, ownershipState string
			exists, tenantIdentity, tenantGeneration, token, ownershipState, _, err = r.inspectTenant(ctx, request.TenantID)
			if err != nil {
				return state, err
			}
			if !exists || token != intent.ID || ownershipState != "completed" {
				return state, fmt.Errorf("created Neon tenant lacks its completed ownership and attachment")
			}
		} else {
			tenantIdentity, tenantGeneration, err = verifiedTenantCreate(response, request.TenantID)
			if err != nil {
				return state, err
			}
		}
		resourceID := tenantIdentity
		if ownershipRequired {
			resourceID, err = encodeNeonOwnedResourceID(tenantIdentity, intent.ID)
			if err != nil {
				return state, err
			}
		}
		tenantClaim, err = r.confirmPending(ctx, pending, intent, resourceID, tenantGeneration)
		if err != nil {
			return state, err
		}
		current[tenantClaim.Component] = tenantClaim
	}
	state.TenantRequested = true
	state.TenantCreated = true
	state.TenantReady = true

	timelineClaim, timelineWasPrior, timelineOwned, err := claimFor(prior, current, "timeline", "neon_timeline")
	if err != nil {
		return state, err
	}
	claimedTimelineIdentity, timelineOwnershipToken := "", ""
	if timelineOwned && ownershipRequired {
		claimedTimelineIdentity, timelineOwnershipToken, err = parseNeonOwnedResourceID(timelineClaim.ResourceID)
		if err != nil {
			return state, fmt.Errorf("Neon timeline ownership claim is invalid: %w", err)
		}
	}
	timelineExists, observedTimelineToken, err := r.inspectTimeline(ctx, request.TenantID, request.TimelineID)
	if err != nil {
		return state, err
	}
	if timelineExists {
		if !timelineOwned {
			intent, pendingCreate, intentErr := pendingIntentFor(pending, "timeline", "neon_timeline", request.TenantID+"/"+request.TimelineID)
			if intentErr != nil {
				return state, intentErr
			}
			if !pendingCreate || r.control.config.allowMissingStorageRegistrationsForTest {
				return state, fmt.Errorf("Neon timeline exists without a confirmed durable ownership claim or recoverable matching pending intent")
			}
			if ownershipRequired && observedTimelineToken != intent.ID {
				return state, fmt.Errorf("Neon timeline exists under a different ownership token")
			}
			var observed bool
			var timelineIdentity string
			var timelineGeneration int64
			var safekeeperOwnershipToken string
			observed, timelineIdentity, timelineGeneration, state.SafekeeperHosts, safekeeperOwnershipToken, err = r.inspectSafekeeperTimeline(ctx, request.TenantID, request.TimelineID)
			if err != nil {
				return state, err
			}
			if ownershipRequired && safekeeperOwnershipToken != intent.ID {
				return state, fmt.Errorf("Neon safekeeper timeline exists under a different ownership token")
			}
			expectedIdentity, identityErr := r.configuredSafekeeperIdentity(request.TenantID, request.TimelineID, timelineGeneration)
			if identityErr != nil || !observed || expectedIdentity != timelineIdentity {
				return state, fmt.Errorf("Neon pending timeline does not match the configured safekeeper identity")
			}
			if err = r.verifyConfiguredSafekeeperHosts(state.SafekeeperHosts); err != nil {
				return state, err
			}
			state.SafekeeperCount = len(state.SafekeeperHosts)
			resourceID := timelineIdentity
			if ownershipRequired {
				resourceID, err = encodeNeonOwnedResourceID(timelineIdentity, intent.ID)
				if err != nil {
					return state, err
				}
			}
			timelineClaim, err = r.confirmPending(ctx, pending, intent, resourceID, timelineGeneration)
			if err != nil {
				return state, err
			}
			current[timelineClaim.Component] = timelineClaim
			timelineOwned = true
			claimedTimelineIdentity, timelineOwnershipToken = timelineIdentity, intent.ID
		}
		if !r.control.config.allowMissingStorageRegistrationsForTest {
			var observed bool
			var timelineIdentity string
			var timelineGeneration int64
			var safekeeperOwnershipToken string
			observed, timelineIdentity, timelineGeneration, state.SafekeeperHosts, safekeeperOwnershipToken, err = r.inspectSafekeeperTimeline(ctx, request.TenantID, request.TimelineID)
			if err != nil {
				return state, err
			}
			if !observed || ownershipRequired && (claimedTimelineIdentity != timelineIdentity || timelineOwnershipToken != observedTimelineToken || timelineOwnershipToken != safekeeperOwnershipToken) || !ownershipRequired && timelineClaim.ResourceID != timelineIdentity || timelineClaim.ImmutableGeneration != timelineGeneration {
				return state, fmt.Errorf("Neon timeline safekeeper identity changed")
			}
			state.SafekeeperCount = len(state.SafekeeperHosts)
		}
		timelineClaim, err = r.activateClaim(ctx, prior, current, timelineClaim, timelineWasPrior)
		if err != nil {
			return state, err
		}
		current[timelineClaim.Component] = timelineClaim
	} else {
		if timelineOwned {
			return state, fmt.Errorf("confirmed Neon timeline disappeared")
		}
		intent, reserveErr := r.reserveOrReuse(ctx, pending, "timeline", "neon_timeline", request.TenantID+"/"+request.TimelineID)
		if reserveErr != nil {
			return state, reserveErr
		}
		if err = r.lifecycle.Heartbeat(ctx); err != nil {
			return state, err
		}
		body := map[string]any{"new_timeline_id": request.TimelineID, "pg_version": 17}
		if request.RecoveryTimelineGeneration > 0 {
			body["generation"] = request.RecoveryTimelineGeneration
		}
		if request.AncestorTimelineID != "" {
			body["ancestor_timeline_id"] = request.AncestorTimelineID
			body["read_only"] = false
		}
		var response []byte
		var createErr error
		if ownershipRequired {
			response, createErr = r.control.doJSONOwned(ctx, r.control.config.StorageController, http.MethodPost, "/v1/tenant/"+request.TenantID+"/timeline", body, intent.ID, http.StatusCreated)
		} else {
			response, createErr = r.control.doJSON(ctx, r.control.config.StorageController, http.MethodPost, "/v1/tenant/"+request.TenantID+"/timeline", body, http.StatusCreated)
		}
		if createErr != nil {
			return state, fmt.Errorf("create Neon timeline left a pending intent: %w", createErr)
		}
		if ownershipRequired {
			if err = verifyNeonOwnershipToken(response, intent.ID); err != nil {
				return state, fmt.Errorf("create Neon timeline returned invalid ownership: %w", err)
			}
		}
		var timelineIdentity string
		var timelineGeneration int64
		timelineIdentity, timelineGeneration, state.SafekeeperHosts, err = verifiedTimelineCreate(response, request.TenantID, request.TimelineID)
		if err != nil {
			return state, err
		}
		state.SafekeeperCount = len(state.SafekeeperHosts)
		if !r.control.config.allowMissingStorageRegistrationsForTest {
			var expectedIdentity string
			expectedIdentity, err = r.configuredSafekeeperIdentity(request.TenantID, request.TimelineID, timelineGeneration)
			if err != nil || expectedIdentity != timelineIdentity {
				return state, fmt.Errorf("Neon timeline placement does not match the configured safekeepers")
			}
			if err = r.verifyConfiguredSafekeeperHosts(state.SafekeeperHosts); err != nil {
				return state, err
			}
		}
		resourceID := timelineIdentity
		if ownershipRequired {
			resourceID, err = encodeNeonOwnedResourceID(timelineIdentity, intent.ID)
			if err != nil {
				return state, err
			}
		}
		timelineClaim, err = r.confirmPending(ctx, pending, intent, resourceID, timelineGeneration)
		if err != nil {
			return state, err
		}
		current[timelineClaim.Component] = timelineClaim
	}
	state.TimelineRequested = true
	state.TimelineCreated = true

	for _, target := range r.control.config.Computes {
		component := "compute-" + target.Name
		externalKey, endpointIdentity, identityErr := neonComputeIdentity(target)
		if identityErr != nil {
			return state, identityErr
		}
		claim, wasPrior, owned, claimErr := claimFor(prior, current, component, "runtime_component")
		if claimErr != nil {
			return state, claimErr
		}
		ownershipToken := ""
		if owned {
			var claimedEndpoint string
			claimedEndpoint, ownershipToken, err = parseNeonComputeClaimResourceID(claim.ResourceID)
			if err != nil || claimedEndpoint != endpointIdentity || claim.ImmutableGeneration != 1 {
				return state, fmt.Errorf("Neon compute %s endpoint identity or ownership token changed", target.Name)
			}
		}
		statusBody, statusCode, statusErr := r.control.request(ctx, target, http.MethodGet, "/status", nil)
		if statusErr != nil {
			return state, statusErr
		}
		attached := statusCode == http.StatusOK && verifyComputeStatus(statusBody, request.TenantID, request.TimelineID) == nil
		detached := statusCode == http.StatusNotFound || statusCode == http.StatusOK && neonComputeDetached(statusBody)
		if !attached && !detached {
			return state, fmt.Errorf("Neon compute %s is attached to another identity", target.Name)
		}
		if owned {
			if statusCode != http.StatusOK {
				return state, fmt.Errorf("Neon compute %s ownership status is unavailable", target.Name)
			}
			if err = verifyNeonComputeOwnershipStatus(statusBody, ownershipToken); err != nil {
				return state, fmt.Errorf("Neon compute %s ownership changed: %w", target.Name, err)
			}
		}
		if attached && !owned {
			intent, pendingConfigure, intentErr := pendingIntentFor(pending, component, "runtime_component", externalKey)
			if intentErr != nil {
				return state, intentErr
			}
			if !pendingConfigure {
				return state, fmt.Errorf("Neon compute %s is configured without a confirmed durable ownership claim or matching pending intent", target.Name)
			}
			if err = verifyNeonComputeOwnershipStatus(statusBody, intent.ID); err != nil {
				return state, fmt.Errorf("Neon compute %s is configured by a different operation: %w", target.Name, err)
			}
			var resourceID string
			resourceID, err = encodeNeonComputeClaimResourceID(endpointIdentity, intent.ID)
			if err != nil {
				return state, err
			}
			claim, err = r.confirmPending(ctx, pending, intent, resourceID, 1)
			if err != nil {
				return state, err
			}
			current[component] = claim
			ownershipToken = intent.ID
			owned = true
		}
		if owned {
			resourceID := claim.ResourceID
			claim, err = r.activateClaim(ctx, prior, current, claim, wasPrior)
			if err != nil {
				return state, err
			}
			if claim.ResourceID != resourceID {
				return state, fmt.Errorf("Neon compute %s ownership changed during revision advance", target.Name)
			}
			current[component] = claim
		}
		if attached {
			// A callback can apply new routing before its transaction commits.
			// A plain resolver read here could still see the previous routing and
			// roll that change back. Running updates belong to the serialized
			// controller callback; lifecycle only verifies its owned identity.
			state.AttachedComputes = append(state.AttachedComputes, target.Name)
			continue
		}
		var intent DurableResourceIntent
		if !owned {
			intent, err = r.reserveOrReuse(ctx, pending, component, "runtime_component", externalKey)
			if err != nil {
				return state, err
			}
			ownershipToken = intent.ID
		}
		if err = r.lifecycle.Heartbeat(ctx); err != nil {
			return state, err
		}
		rawConfig := request.ComputeConfig[target.Name]
		if r.control.config.ResolveComputeConfig != nil {
			rawConfig, err = r.control.config.ResolveComputeConfig(ctx, target.Name, rawConfig)
			if err != nil {
				return state, err
			}
		}
		ownedConfig, bindErr := bindNeonComputeOwnership(rawConfig, ownershipToken)
		if bindErr != nil {
			return state, bindErr
		}
		if r.control.config.ResolveComputeConfig != nil {
			err = r.control.configureOwnedCompute(ctx, target, ownedConfig, ownershipToken, request.TenantID, request.TimelineID, statusBody, !owned)
		} else {
			_, err = r.control.doRawJSONOwned(ctx, target, http.MethodPost, "/configure", ownedConfig, ownershipToken, http.StatusOK)
		}
		if err != nil {
			return state, fmt.Errorf("configure Neon compute %s left ownership pending: %w", target.Name, err)
		}
		status, statusErr := r.control.doJSON(ctx, target, http.MethodGet, "/status", nil, http.StatusOK)
		if statusErr != nil {
			return state, statusErr
		}
		if err = verifyComputeStatus(status, request.TenantID, request.TimelineID); err != nil {
			return state, err
		}
		if err = verifyNeonComputeOwnershipStatus(status, ownershipToken); err != nil {
			return state, err
		}
		if !owned {
			var resourceID string
			resourceID, err = encodeNeonComputeClaimResourceID(endpointIdentity, ownershipToken)
			if err != nil {
				return state, err
			}
			claim, err = r.confirmPending(ctx, pending, intent, resourceID, 1)
			if err != nil {
				return state, err
			}
			current[component] = claim
		}
		state.AttachedComputes = append(state.AttachedComputes, target.Name)
	}
	if len(pending) != 0 {
		return state, fmt.Errorf("Neon pending intent inventory does not match the configured runtime")
	}
	if len(prior) != 0 || len(current) != len(r.control.config.Computes)+len(r.control.config.Pageservers)+len(r.control.config.Safekeepers)+2 {
		return state, fmt.Errorf("Neon ownership inventory does not match the configured runtime")
	}
	sort.Strings(state.AttachedComputes)
	state.Complete = true
	state.UpdatedAt = time.Now().UTC()
	return state, nil
}

// Deprovision performs a complete identity preflight before any mutation.
// Timeline ownership is re-observed from every configured safekeeper because
// the storage controller's timeline inspection omits membership generation.
func (r *DurableNeonRuntime) Deprovision(ctx context.Context, request NeonLifecycleRequest) error {
	op := r.lifecycle.Operation()
	if op.Kind != "delete" || request.OperationID != op.ID {
		return fmt.Errorf("Neon deletion request does not match the leased operation")
	}
	if _, err := validateNeonLifecycleRequest(request, r.control.config.Computes); err != nil {
		return err
	}
	// A failed initial provision may have created only Kubernetes objects. The
	// durable store must check all revisions under the delete lease before an
	// unavailable provider can be skipped; revision-local reads are insufficient.
	if state, ok := r.lifecycle.(interface {
		NeonProviderStateEmpty(context.Context) (bool, error)
	}); ok {
		empty, err := state.NeonProviderStateEmpty(ctx)
		if err != nil {
			return err
		}
		if empty {
			return r.lifecycle.Heartbeat(ctx)
		}
	}
	if err := r.control.verifyOwnershipCapability(ctx); err != nil {
		return err
	}
	ownershipRequired := !r.control.config.allowUnqualifiedOwnershipProtocolForTest
	prior, current, err := r.claims(ctx)
	if err != nil {
		return err
	}
	expected := map[string]string{"tenant": "neon_tenant", "timeline": "neon_timeline"}
	for _, node := range r.control.config.Pageservers {
		expected["pageserver-registration-"+node.Name] = "runtime_component"
	}
	for _, node := range r.control.config.Safekeepers {
		expected["safekeeper-registration-"+node.Name] = "runtime_component"
	}
	for _, target := range r.control.config.Computes {
		expected["compute-"+target.Name] = "runtime_component"
	}
	claims := map[string]DurableResourceClaim{}
	wasPrior := map[string]bool{}
	partial, _ := r.lifecycle.(interface{ AllowPartialNeonDeprovision() bool })
	for component, claim := range prior {
		if kind, ok := expected[component]; !ok || claim.Kind != kind {
			return fmt.Errorf("Neon deletion claim inventory contains an unexpected resource")
		}
	}
	for component, claim := range current {
		if kind, ok := expected[component]; !ok || claim.Kind != kind {
			return fmt.Errorf("Neon deletion claim inventory contains an unexpected resource")
		}
	}
	for component, kind := range expected {
		claim, priorClaim, ok, claimErr := claimFor(prior, current, component, kind)
		if claimErr != nil {
			return claimErr
		}
		if !ok && (strings.HasPrefix(component, "pageserver-registration-") || strings.HasPrefix(component, "safekeeper-registration-")) && (partial == nil || !partial.AllowPartialNeonDeprovision()) {
			return fmt.Errorf("Neon %s ownership claim is missing", component)
		}
		if ok {
			claims[component] = claim
			wasPrior[component] = priorClaim
		}
	}

	tenantExists, tenantIdentity, tenantGeneration, observedTenantToken, tenantOwnershipState, observedDeletionToken, err := r.inspectTenant(ctx, request.TenantID)
	if err != nil {
		return err
	}
	tenantClaim, tenantClaimed := claims["tenant"]
	if tenantExists {
		claimedIdentity, ownershipToken := tenantClaim.ResourceID, ""
		if tenantClaimed && ownershipRequired {
			claimedIdentity, ownershipToken, err = parseNeonOwnedResourceID(tenantClaim.ResourceID)
		}
		if !tenantClaimed || err != nil || ownershipRequired && ownershipToken != observedTenantToken {
			return fmt.Errorf("Neon tenant attachment identity changed or is unclaimed; deletion refused")
		}
		if tenantOwnershipState != "deleting" && (claimedIdentity != tenantIdentity || tenantClaim.ImmutableGeneration != tenantGeneration) {
			return fmt.Errorf("Neon tenant attachment identity changed or is unclaimed; deletion refused")
		}
	}
	timelineExists, observedTimelineToken, err := r.inspectTimeline(ctx, request.TenantID, request.TimelineID)
	if err != nil {
		return err
	}
	safekeeperTimelineExists, timelineIdentity, timelineGeneration, _, observedSafekeeperToken, err := r.inspectSafekeeperTimeline(ctx, request.TenantID, request.TimelineID)
	if err != nil {
		return err
	}
	preparedDeletion := ownershipRequired && tenantOwnershipState == "deleting" && validNeonComputeOwnershipToken(observedDeletionToken)
	if timelineExists != safekeeperTimelineExists && !preparedDeletion {
		return fmt.Errorf("Neon controller and safekeeper timeline presence is inconsistent")
	}
	if timelineExists || safekeeperTimelineExists {
		timelineClaim, timelineClaimed := claims["timeline"]
		claimedIdentity, ownershipToken := timelineClaim.ResourceID, ""
		if timelineClaimed && ownershipRequired {
			claimedIdentity, ownershipToken, err = parseNeonOwnedResourceID(timelineClaim.ResourceID)
		}
		if !timelineClaimed || err != nil || claimedIdentity != timelineIdentity || timelineClaim.ImmutableGeneration != timelineGeneration || ownershipRequired && (timelineExists && ownershipToken != observedTimelineToken || safekeeperTimelineExists && ownershipToken != observedSafekeeperToken) {
			return fmt.Errorf("Neon timeline safekeeper identity changed; deletion refused")
		}
	}
	timelineExists = timelineExists || safekeeperTimelineExists
	for _, node := range r.control.config.Pageservers {
		claim, claimed := claims["pageserver-registration-"+node.Name]
		if !claimed && partial != nil && partial.AllowPartialNeonDeprovision() {
			continue
		}
		body, status, requestErr := r.control.request(ctx, r.control.config.StorageController, http.MethodGet, "/control/v1/node/"+strconv.FormatInt(node.NodeID, 10), nil)
		if requestErr != nil {
			return requestErr
		}
		if status == http.StatusNotFound {
			continue
		}
		if status != http.StatusOK {
			return fmt.Errorf("inspect Neon pageserver %s for deletion returned HTTP %d", node.Name, status)
		}
		identity, identityErr := verifiedNeonPageserverRegistrationIdentity(body, node)
		claimedIdentity := claim.ResourceID
		if ownershipRequired {
			var ownershipToken string
			claimedIdentity, ownershipToken, err = parseNeonOwnedResourceID(claim.ResourceID)
			if err != nil || verifyNeonOwnershipToken(body, ownershipToken) != nil {
				return fmt.Errorf("Neon pageserver %s registration ownership changed; deletion refused", node.Name)
			}
		}
		if identityErr != nil || claimedIdentity != identity || claim.ImmutableGeneration != node.Generation {
			return fmt.Errorf("Neon pageserver %s registration identity changed; deletion refused", node.Name)
		}
	}
	for _, node := range r.control.config.Safekeepers {
		claim, claimed := claims["safekeeper-registration-"+node.Name]
		if !claimed && partial != nil && partial.AllowPartialNeonDeprovision() {
			continue
		}
		body, status, requestErr := r.control.request(ctx, r.control.config.StorageController, http.MethodGet, "/control/v1/safekeeper/"+strconv.FormatInt(node.NodeID, 10), nil)
		if requestErr != nil {
			return requestErr
		}
		if status == http.StatusNotFound {
			continue
		}
		if status != http.StatusOK {
			return fmt.Errorf("inspect Neon safekeeper %s for deletion returned HTTP %d", node.Name, status)
		}
		identity, identityErr := verifiedNeonSafekeeperRegistrationIdentity(body, node)
		claimedIdentity := claim.ResourceID
		if ownershipRequired {
			var ownershipToken string
			claimedIdentity, ownershipToken, err = parseNeonOwnedResourceID(claim.ResourceID)
			if err != nil || verifyNeonOwnershipToken(body, ownershipToken) != nil {
				return fmt.Errorf("Neon safekeeper %s registration ownership changed; deletion refused", node.Name)
			}
		}
		if identityErr != nil || claimedIdentity != identity || claim.ImmutableGeneration != node.Generation {
			return fmt.Errorf("Neon safekeeper %s registration identity changed; deletion refused", node.Name)
		}
	}
	computeOwnershipTokens := map[string]string{}
	for _, target := range r.control.config.Computes {
		component := "compute-" + target.Name
		_, endpointIdentity, identityErr := neonComputeIdentity(target)
		if identityErr != nil {
			return identityErr
		}
		claim, claimed := claims[component]
		if claimed {
			claimedEndpoint, ownershipToken, parseErr := parseNeonComputeClaimResourceID(claim.ResourceID)
			if parseErr != nil || claimedEndpoint != endpointIdentity || claim.ImmutableGeneration != 1 {
				return fmt.Errorf("Neon compute %s endpoint identity or ownership token changed; deletion refused", target.Name)
			}
			computeOwnershipTokens[component] = ownershipToken
		}
		body, status, requestErr := r.control.request(ctx, target, http.MethodGet, "/status", nil)
		if requestErr != nil {
			return requestErr
		}
		if status != http.StatusNotFound && (status != http.StatusOK || verifyComputeStatus(body, request.TenantID, request.TimelineID) != nil && !neonComputeDetached(body)) {
			return fmt.Errorf("Neon compute %s identity changed; deletion refused", target.Name)
		}
		if status == http.StatusOK && !neonComputeDetached(body) && !claimed {
			return fmt.Errorf("Neon compute %s is attached without an ownership claim; deletion refused", target.Name)
		}
		if claimed {
			if status != http.StatusOK {
				return fmt.Errorf("Neon compute %s ownership status is unavailable; deletion refused", target.Name)
			}
			if err = verifyNeonComputeOwnershipStatus(body, computeOwnershipTokens[component]); err != nil {
				return fmt.Errorf("Neon compute %s ownership changed; deletion refused: %w", target.Name, err)
			}
		}
	}
	tenantDeletionToken := ""
	if tenantExists && ownershipRequired {
		_, ownershipToken, parseErr := parseNeonOwnedResourceID(claims["tenant"].ResourceID)
		if parseErr != nil {
			return parseErr
		}
		_, status, preflightErr := r.control.requestOwned(ctx, r.control.config.StorageController, http.MethodDelete, "/v1/tenant/"+request.TenantID+"?validate_only=true", nil, ownershipToken)
		if preflightErr != nil {
			return fmt.Errorf("preflight Neon tenant deletion: %w", preflightErr)
		}
		if status != http.StatusOK {
			return fmt.Errorf("preflight Neon tenant deletion returned HTTP %d", status)
		}
		prepared, status, prepareErr := r.control.requestOwned(ctx, r.control.config.StorageController, http.MethodDelete, "/v1/tenant/"+request.TenantID+"?prepare_delete=true", nil, ownershipToken)
		if prepareErr != nil {
			return fmt.Errorf("prepare Neon tenant deletion: %w", prepareErr)
		}
		if status != http.StatusOK {
			return fmt.Errorf("prepare Neon tenant deletion returned HTTP %d", status)
		}
		var preparation struct {
			SchemaVersion int    `json:"schema_version"`
			Digest        string `json:"digest"`
			DeleteToken   string `json:"delete_token"`
		}
		if err = json.Unmarshal(prepared, &preparation); err != nil || preparation.SchemaVersion != 1 || !validLowerHex(preparation.Digest, 64) || !validNeonComputeOwnershipToken(preparation.DeleteToken) {
			return fmt.Errorf("prepare Neon tenant deletion returned an invalid fence")
		}
		if observedDeletionToken != "" && observedDeletionToken != preparation.DeleteToken {
			return fmt.Errorf("prepare Neon tenant deletion returned a conflicting retry token")
		}
		tenantDeletionToken = preparation.DeleteToken
	}

	for component, claim := range claims {
		resourceID := claim.ResourceID
		claim, err = r.activateClaim(ctx, prior, current, claim, wasPrior[component])
		if err != nil {
			return err
		}
		if strings.HasPrefix(component, "compute-") && claim.ResourceID != resourceID {
			return fmt.Errorf("Neon compute ownership changed during revision advance")
		}
		claims[component] = claim
	}
	for i := len(r.control.config.Computes) - 1; i >= 0; i-- {
		target := r.control.config.Computes[i]
		component := "compute-" + target.Name
		claim, claimed := claims[component]
		if claimed {
			if err = r.terminateOwnedCompute(ctx, target, request.TenantID, request.TimelineID, computeOwnershipTokens[component]); err != nil {
				return err
			}
			if err = r.lifecycle.Release(ctx, claim); err != nil {
				return err
			}
			continue
		}
		body, status, requestErr := r.control.request(ctx, target, http.MethodGet, "/status", nil)
		if requestErr != nil || status != http.StatusNotFound && (status != http.StatusOK || !neonComputeDetached(body)) {
			return fmt.Errorf("Neon compute %s became attached without an ownership claim; deletion refused", target.Name)
		}
	}
	if timelineExists {
		if err = r.lifecycle.Heartbeat(ctx); err != nil {
			return err
		}
		var status int
		var deleteErr error
		if ownershipRequired {
			_, ownershipToken, parseErr := parseNeonOwnedResourceID(claims["timeline"].ResourceID)
			if parseErr != nil {
				return parseErr
			}
			_, status, deleteErr = r.control.requestOwned(ctx, r.control.config.StorageController, http.MethodDelete, "/v1/tenant/"+request.TenantID+"/timeline/"+request.TimelineID, nil, ownershipToken)
		} else {
			_, status, deleteErr = r.control.request(ctx, r.control.config.StorageController, http.MethodDelete, "/v1/tenant/"+request.TenantID+"/timeline/"+request.TimelineID, nil)
		}
		if deleteErr != nil || status != http.StatusOK && status != http.StatusAccepted && status != http.StatusNoContent && status != http.StatusNotFound && status != http.StatusConflict {
			return fmt.Errorf("delete Neon timeline returned HTTP %d: %w", status, deleteErr)
		}
		timelineExists, _, err = r.inspectTimeline(ctx, request.TenantID, request.TimelineID)
		if err != nil {
			return err
		}
		safekeeperTimelineExists, _, _, _, _, err = r.inspectSafekeeperTimeline(ctx, request.TenantID, request.TimelineID)
		if err != nil {
			return err
		}
		if timelineExists || safekeeperTimelineExists {
			return fmt.Errorf("Neon timeline deletion is still in progress")
		}
	}
	if claim, ok := claims["timeline"]; ok {
		if err = r.lifecycle.Release(ctx, claim); err != nil {
			return err
		}
	}
	if tenantExists {
		if err = r.lifecycle.Heartbeat(ctx); err != nil {
			return err
		}
		var status int
		var deleteErr error
		if ownershipRequired {
			_, ownershipToken, parseErr := parseNeonOwnedResourceID(claims["tenant"].ResourceID)
			if parseErr != nil {
				return parseErr
			}
			_, status, deleteErr = r.control.requestPreparedDelete(ctx, r.control.config.StorageController, http.MethodDelete, "/v1/tenant/"+request.TenantID, nil, ownershipToken, tenantDeletionToken)
		} else {
			_, status, deleteErr = r.control.request(ctx, r.control.config.StorageController, http.MethodDelete, "/v1/tenant/"+request.TenantID, nil)
		}
		if deleteErr != nil || status != http.StatusOK && status != http.StatusAccepted && status != http.StatusNoContent && status != http.StatusNotFound {
			return fmt.Errorf("delete Neon tenant returned HTTP %d: %w", status, deleteErr)
		}
		tenantExists, _, _, _, _, _, err = r.inspectTenant(ctx, request.TenantID)
		if err != nil {
			return err
		}
		if tenantExists {
			return fmt.Errorf("Neon tenant deletion is still in progress")
		}
	}
	if claim, ok := claims["tenant"]; ok {
		if err = r.lifecycle.Release(ctx, claim); err != nil {
			return err
		}
	}
	// Storage-controller registration claims remain until Kubernetes confirms
	// the owned namespace is gone. This prevents a crash between individual
	// releases from leaving a live registration without an ownership claim.
	return nil
}

// InspectOwnedRecoveryReservation recovers the exact provider identity left
// between a durable reservation and confirmation. It performs only GETs and
// accepts an identity only when its deterministic external key and ownership
// token both match the reserved intent.
func (r *DurableNeonRuntime) InspectOwnedRecoveryReservation(ctx context.Context, request NeonLifecycleRequest, intent DurableResourceIntent) (string, int64, bool, error) {
	op := r.lifecycle.Operation()
	if request.OperationID != op.ID || intent.ID == "" || intent.PlatformID != op.PlatformID || intent.PlatformRevision != op.Revision || intent.OwnerOperationID != op.ID || intent.Confirmed {
		return "", 0, false, fmt.Errorf("Neon recovery reservation does not match the leased operation")
	}
	if _, err := validateNeonLifecycleRequest(request, r.control.config.Computes); err != nil {
		return "", 0, false, err
	}
	switch {
	case intent.Component == "tenant" && intent.Kind == "neon_tenant" && intent.ExternalKey == request.TenantID:
		exists, identity, generation, token, _, _, err := r.inspectTenant(ctx, request.TenantID)
		if err != nil || !exists {
			return "", 0, exists, err
		}
		if token != intent.ID || request.RecoveryTenantGeneration > 0 && generation != request.RecoveryTenantGeneration {
			return "", 0, false, fmt.Errorf("Neon tenant recovery reservation ownership changed")
		}
		resourceID, err := encodeNeonOwnedResourceID(identity, intent.ID)
		return resourceID, generation, true, err
	case intent.Component == "timeline" && intent.Kind == "neon_timeline" && intent.ExternalKey == request.TenantID+"/"+request.TimelineID:
		exists, controllerToken, err := r.inspectTimeline(ctx, request.TenantID, request.TimelineID)
		if err != nil {
			return "", 0, false, err
		}
		safekeeperExists, identity, generation, _, safekeeperToken, err := r.inspectSafekeeperTimeline(ctx, request.TenantID, request.TimelineID)
		if err != nil || !exists && !safekeeperExists {
			return "", 0, false, err
		}
		if !exists || !safekeeperExists || controllerToken != intent.ID || safekeeperToken != intent.ID || request.RecoveryTimelineGeneration > 0 && generation != request.RecoveryTimelineGeneration {
			return "", 0, false, fmt.Errorf("Neon timeline recovery reservation ownership changed")
		}
		resourceID, err := encodeNeonOwnedResourceID(identity, intent.ID)
		return resourceID, generation, true, err
	case strings.HasPrefix(intent.Component, "compute-") && intent.Kind == "runtime_component":
		for _, target := range r.control.config.Computes {
			component := "compute-" + target.Name
			externalKey, endpointIdentity, err := neonComputeIdentity(target)
			if err != nil || component != intent.Component || externalKey != intent.ExternalKey {
				continue
			}
			body, status, requestErr := r.control.request(ctx, target, http.MethodGet, "/status", nil)
			if requestErr != nil {
				return "", 0, false, requestErr
			}
			if status == http.StatusNotFound || status == http.StatusOK && neonComputeDetached(body) {
				return "", 0, false, nil
			}
			if status != http.StatusOK || verifyComputeStatus(body, request.TenantID, request.TimelineID) != nil || verifyNeonComputeOwnershipStatus(body, intent.ID) != nil {
				return "", 0, false, fmt.Errorf("Neon compute recovery reservation ownership changed")
			}
			resourceID, err := encodeNeonComputeClaimResourceID(endpointIdentity, intent.ID)
			return resourceID, 1, true, err
		}
	case strings.HasPrefix(intent.Component, "pageserver-registration-") && intent.Kind == "runtime_component":
		for _, node := range r.control.config.Pageservers {
			externalKey := neonStorageIdentity("pageserver", node.Name, node.NodeID, node.Generation, node.Host, node.AvailabilityZone)
			if "pageserver-registration-"+node.Name != intent.Component || intent.ExternalKey != externalKey {
				continue
			}
			body, status, err := r.control.request(ctx, r.control.config.StorageController, http.MethodGet, "/control/v1/node/"+strconv.FormatInt(node.NodeID, 10), nil)
			if err != nil || status == http.StatusNotFound {
				return "", 0, false, err
			}
			identity, identityErr := verifiedNeonPageserverRegistrationIdentity(body, node)
			if status != http.StatusOK || identityErr != nil || verifyNeonOwnershipToken(body, intent.ID) != nil {
				return "", 0, false, fmt.Errorf("Neon pageserver recovery reservation ownership changed")
			}
			resourceID, err := encodeNeonOwnedResourceID(identity, intent.ID)
			return resourceID, node.Generation, true, err
		}
	case strings.HasPrefix(intent.Component, "safekeeper-registration-") && intent.Kind == "runtime_component":
		for _, node := range r.control.config.Safekeepers {
			externalKey := neonStorageIdentity("safekeeper", node.Name, node.NodeID, node.Generation, node.Host, node.AvailabilityZone)
			if "safekeeper-registration-"+node.Name != intent.Component || intent.ExternalKey != externalKey {
				continue
			}
			body, status, err := r.control.request(ctx, r.control.config.StorageController, http.MethodGet, "/control/v1/safekeeper/"+strconv.FormatInt(node.NodeID, 10), nil)
			if err != nil || status == http.StatusNotFound {
				return "", 0, false, err
			}
			identity, identityErr := verifiedNeonSafekeeperRegistrationIdentity(body, node)
			if status != http.StatusOK || identityErr != nil || verifyNeonOwnershipToken(body, intent.ID) != nil {
				return "", 0, false, fmt.Errorf("Neon safekeeper recovery reservation ownership changed")
			}
			resourceID, err := encodeNeonOwnedResourceID(identity, intent.ID)
			return resourceID, node.Generation, true, err
		}
	}
	return "", 0, false, fmt.Errorf("Neon recovery reservation intent is invalid")
}

// InspectOwnedRecoveryClaim determines whether an original provider identity
// still exists without changing it. An identity mismatch is an error rather
// than absence so cleanup never adopts or forgets a foreign resource.
func (r *DurableNeonRuntime) InspectOwnedRecoveryClaim(ctx context.Context, request NeonLifecycleRequest, claim DurableResourceClaim) (bool, error) {
	op := r.lifecycle.Operation()
	if request.OperationID != op.ID || claim.PlatformID != op.PlatformID || claim.PlatformRevision != op.Revision || claim.OwnerOperationID != op.ID {
		return false, fmt.Errorf("Neon recovery claim does not match the accepted operation")
	}
	if _, err := validateNeonLifecycleRequest(request, r.control.config.Computes); err != nil {
		return false, err
	}
	switch {
	case claim.Component == "tenant" && claim.Kind == "neon_tenant":
		claimedIdentity, token, err := parseNeonOwnedResourceID(claim.ResourceID)
		if err != nil {
			return false, err
		}
		exists, identity, generation, observedToken, _, _, err := r.inspectTenant(ctx, request.TenantID)
		if err != nil || !exists {
			return false, err
		}
		if identity != claimedIdentity || generation != claim.ImmutableGeneration || observedToken != token {
			return false, fmt.Errorf("Neon prior tenant ownership changed")
		}
		return true, nil
	case claim.Component == "timeline" && claim.Kind == "neon_timeline":
		claimedIdentity, token, err := parseNeonOwnedResourceID(claim.ResourceID)
		if err != nil {
			return false, err
		}
		exists, controllerToken, err := r.inspectTimeline(ctx, request.TenantID, request.TimelineID)
		if err != nil {
			return false, err
		}
		safekeeperExists, identity, generation, _, safekeeperToken, err := r.inspectSafekeeperTimeline(ctx, request.TenantID, request.TimelineID)
		if err != nil {
			return false, err
		}
		if !exists && !safekeeperExists {
			return false, nil
		}
		if !exists || !safekeeperExists || identity != claimedIdentity || generation != claim.ImmutableGeneration || controllerToken != token || safekeeperToken != token {
			return false, fmt.Errorf("Neon prior timeline ownership changed")
		}
		return true, nil
	case strings.HasPrefix(claim.Component, "compute-") && claim.Kind == "runtime_component":
		for _, target := range r.control.config.Computes {
			if "compute-"+target.Name != claim.Component {
				continue
			}
			_, expectedIdentity, err := neonComputeIdentity(target)
			if err != nil {
				return false, err
			}
			claimedIdentity, token, err := parseNeonComputeClaimResourceID(claim.ResourceID)
			if err != nil || claimedIdentity != expectedIdentity || claim.ImmutableGeneration != 1 {
				return false, fmt.Errorf("Neon prior compute claim changed")
			}
			body, status, err := r.control.request(ctx, target, http.MethodGet, "/status", nil)
			if err != nil {
				return false, err
			}
			if status == http.StatusNotFound || status == http.StatusOK && neonComputeDetached(body) {
				return false, nil
			}
			if status != http.StatusOK || verifyComputeStatus(body, request.TenantID, request.TimelineID) != nil || verifyNeonComputeOwnershipStatus(body, token) != nil {
				return false, fmt.Errorf("Neon prior compute ownership changed")
			}
			return true, nil
		}
	}
	return false, fmt.Errorf("Neon prior recovery claim is invalid")
}

func (r *DurableNeonRuntime) terminateOwnedCompute(ctx context.Context, target NeonControlTarget, tenantID, timelineID, ownershipToken string) error {
	body, status, requestErr := r.control.request(ctx, target, http.MethodGet, "/status", nil)
	if requestErr != nil {
		return requestErr
	}
	if status != http.StatusOK {
		return fmt.Errorf("Neon compute %s ownership status is unavailable immediately before termination", target.Name)
	}
	if err := verifyNeonComputeOwnershipStatus(body, ownershipToken); err != nil {
		return fmt.Errorf("Neon compute %s ownership changed immediately before termination: %w", target.Name, err)
	}
	if neonComputeDetached(body) {
		return nil
	}
	if err := verifyComputeStatus(body, tenantID, timelineID); err != nil {
		return fmt.Errorf("Neon compute %s identity changed immediately before termination: %w", target.Name, err)
	}
	if err := r.lifecycle.Heartbeat(ctx); err != nil {
		return err
	}
	_, status, requestErr = r.control.requestOwned(ctx, target, http.MethodPost, "/terminate?mode=immediate", nil, ownershipToken)
	if requestErr != nil || status != http.StatusOK && status != http.StatusCreated {
		return fmt.Errorf("terminate Neon compute %s returned HTTP %d: %w", target.Name, status, requestErr)
	}
	body, status, requestErr = r.control.request(ctx, target, http.MethodGet, "/status", nil)
	if requestErr != nil || status != http.StatusOK || !neonComputeDetached(body) {
		return fmt.Errorf("Neon compute %s is still attached", target.Name)
	}
	if err := verifyNeonComputeOwnershipStatus(body, ownershipToken); err != nil {
		return fmt.Errorf("Neon compute %s ownership changed after termination: %w", target.Name, err)
	}
	return nil
}

func validateNeonStorageNode(name string, nodeID, generation int64, host, zone string) error {
	if err := validateNeonStorageEndpoint(name, nodeID, generation, host); err != nil {
		return err
	}
	if zone == "" || len(zone) > 63 || len(validation.IsDNS1123Label(zone)) != 0 {
		return fmt.Errorf("storage node requires a stable name, positive node ID and generation, DNS hostname, and availability zone")
	}
	return nil
}

func validateNeonStorageEndpoint(name string, nodeID, generation int64, host string) error {
	if name == "" || len(name) > 63 || len(validation.IsDNS1123Label(name)) != 0 || nodeID < 1 || generation < 1 || host == "" || len(host) > 253 || net.ParseIP(host) != nil || len(validation.IsDNS1123Subdomain(host)) != 0 {
		return fmt.Errorf("storage node requires a stable name, positive node ID and generation, and DNS hostname")
	}
	return nil
}

func neonStorageIdentity(kind, name string, nodeID, generation int64, host, zone string) string {
	sum := sha256.Sum256([]byte(kind + "\x00" + name + "\x00" + strconv.FormatInt(nodeID, 10) + "\x00" + strconv.FormatInt(generation, 10) + "\x00" + host + "\x00" + zone))
	return hex.EncodeToString(sum[:])
}

func (r *DurableNeonRuntime) registerStorageNodes(ctx context.Context, prior, current map[string]DurableResourceClaim, pending map[string]DurableResourceIntent) error {
	controller := r.control.config.StorageController
	ownershipRequired := !r.control.config.allowUnqualifiedOwnershipProtocolForTest
	for _, node := range r.control.config.Pageservers {
		component := "pageserver-registration-" + node.Name
		identity := neonStorageIdentity("pageserver", node.Name, node.NodeID, node.Generation, node.Host, node.AvailabilityZone)
		claim, wasPrior, owned, err := claimFor(prior, current, component, "runtime_component")
		if err != nil {
			return err
		}
		observed, status, err := r.control.request(ctx, controller, http.MethodGet, "/control/v1/node/"+strconv.FormatInt(node.NodeID, 10), nil)
		if err != nil {
			return err
		}
		exists := status == http.StatusOK
		if status != http.StatusOK && status != http.StatusNotFound {
			return fmt.Errorf("inspect Neon pageserver %s returned HTTP %d", node.Name, status)
		}
		if exists {
			if err = verifyNeonPageserverRegistration(observed, node); err != nil {
				return err
			}
			if !owned {
				intent, pendingCreate, intentErr := pendingIntentFor(pending, component, "runtime_component", identity)
				if intentErr != nil {
					return intentErr
				}
				if !pendingCreate {
					return fmt.Errorf("Neon pageserver %s is registered without a confirmed durable ownership claim or matching pending intent", node.Name)
				}
				if ownershipRequired {
					if err = verifyNeonOwnershipToken(observed, intent.ID); err != nil {
						return fmt.Errorf("Neon pageserver %s is registered under a different ownership token: %w", node.Name, err)
					}
					identity, err = encodeNeonOwnedResourceID(identity, intent.ID)
					if err != nil {
						return err
					}
				}
				claim, err = r.confirmPending(ctx, pending, intent, identity, node.Generation)
				if err != nil {
					return err
				}
				current[component] = claim
				owned = true
			}
		} else if owned {
			return fmt.Errorf("confirmed Neon pageserver %s registration disappeared", node.Name)
		}
		if owned {
			claimedIdentity := claim.ResourceID
			if ownershipRequired {
				var ownershipToken string
				claimedIdentity, ownershipToken, err = parseNeonOwnedResourceID(claim.ResourceID)
				if err != nil || verifyNeonOwnershipToken(observed, ownershipToken) != nil {
					return fmt.Errorf("Neon pageserver %s registration ownership changed", node.Name)
				}
			}
			if claimedIdentity != neonStorageIdentity("pageserver", node.Name, node.NodeID, node.Generation, node.Host, node.AvailabilityZone) || claim.ImmutableGeneration != node.Generation {
				return fmt.Errorf("Neon pageserver %s registration identity changed", node.Name)
			}
			claim, err = r.activateClaim(ctx, prior, current, claim, wasPrior)
			if err != nil {
				return err
			}
			current[component] = claim
			continue
		}
		intent, err := r.reserveOrReuse(ctx, pending, component, "runtime_component", identity)
		if err != nil {
			return err
		}
		if err = r.lifecycle.Heartbeat(ctx); err != nil {
			return err
		}
		body := map[string]any{"node_id": node.NodeID, "listen_pg_addr": node.Host, "listen_pg_port": 6400, "listen_grpc_addr": nil, "listen_grpc_port": nil, "listen_http_addr": node.Host, "listen_http_port": 9897, "listen_https_port": 9898, "availability_zone_id": node.AvailabilityZone, "node_ip_addr": nil}
		var created []byte
		if ownershipRequired {
			created, err = r.control.doJSONOwned(ctx, controller, http.MethodPost, "/control/v1/node", body, intent.ID, http.StatusOK)
		} else {
			created, err = r.control.doJSON(ctx, controller, http.MethodPost, "/control/v1/node", body, http.StatusOK)
		}
		if err != nil {
			return fmt.Errorf("register Neon pageserver %s left ownership pending: %w", node.Name, err)
		}
		if ownershipRequired {
			if err = verifyNeonOwnershipToken(created, intent.ID); err != nil {
				return fmt.Errorf("register Neon pageserver %s returned invalid ownership: %w", node.Name, err)
			}
		}
		observed, err = r.control.doJSON(ctx, controller, http.MethodGet, "/control/v1/node/"+strconv.FormatInt(node.NodeID, 10), nil, http.StatusOK)
		if err != nil {
			return err
		}
		if err = verifyNeonPageserverRegistration(observed, node); err != nil {
			return err
		}
		if ownershipRequired {
			if err = verifyNeonOwnershipToken(observed, intent.ID); err != nil {
				return fmt.Errorf("inspect Neon pageserver %s returned invalid ownership: %w", node.Name, err)
			}
			identity, err = encodeNeonOwnedResourceID(identity, intent.ID)
			if err != nil {
				return err
			}
		}
		claim, err = r.confirmPending(ctx, pending, intent, identity, node.Generation)
		if err != nil {
			return err
		}
		current[component] = claim
	}
	for _, node := range r.control.config.Safekeepers {
		component := "safekeeper-registration-" + node.Name
		identity := neonStorageIdentity("safekeeper", node.Name, node.NodeID, node.Generation, node.Host, node.AvailabilityZone)
		claim, wasPrior, owned, err := claimFor(prior, current, component, "runtime_component")
		if err != nil {
			return err
		}
		observed, status, err := r.control.request(ctx, controller, http.MethodGet, "/control/v1/safekeeper/"+strconv.FormatInt(node.NodeID, 10), nil)
		if err != nil {
			return err
		}
		exists := status == http.StatusOK
		if status != http.StatusOK && status != http.StatusNotFound {
			return fmt.Errorf("inspect Neon safekeeper %s returned HTTP %d", node.Name, status)
		}
		if exists {
			if err = verifyNeonSafekeeperRegistration(observed, node); err != nil {
				return err
			}
			if !owned {
				intent, pendingCreate, intentErr := pendingIntentFor(pending, component, "runtime_component", identity)
				if intentErr != nil {
					return intentErr
				}
				if !pendingCreate {
					return fmt.Errorf("Neon safekeeper %s is registered without a confirmed durable ownership claim or matching pending intent", node.Name)
				}
				if ownershipRequired {
					if err = verifyNeonOwnershipToken(observed, intent.ID); err != nil {
						return fmt.Errorf("Neon safekeeper %s is registered under a different ownership token: %w", node.Name, err)
					}
					identity, err = encodeNeonOwnedResourceID(identity, intent.ID)
					if err != nil {
						return err
					}
				}
				claim, err = r.confirmPending(ctx, pending, intent, identity, node.Generation)
				if err != nil {
					return err
				}
				current[component] = claim
				owned = true
			}
		} else if owned {
			return fmt.Errorf("confirmed Neon safekeeper %s registration disappeared", node.Name)
		}
		if owned {
			claimedIdentity := claim.ResourceID
			if ownershipRequired {
				var ownershipToken string
				claimedIdentity, ownershipToken, err = parseNeonOwnedResourceID(claim.ResourceID)
				if err != nil || verifyNeonOwnershipToken(observed, ownershipToken) != nil {
					return fmt.Errorf("Neon safekeeper %s registration ownership changed", node.Name)
				}
			}
			if claimedIdentity != neonStorageIdentity("safekeeper", node.Name, node.NodeID, node.Generation, node.Host, node.AvailabilityZone) || claim.ImmutableGeneration != node.Generation {
				return fmt.Errorf("Neon safekeeper %s registration identity changed", node.Name)
			}
			claim, err = r.activateClaim(ctx, prior, current, claim, wasPrior)
			if err != nil {
				return err
			}
			current[component] = claim
			continue
		}
		intent, err := r.reserveOrReuse(ctx, pending, component, "runtime_component", identity)
		if err != nil {
			return err
		}
		if err = r.lifecycle.Heartbeat(ctx); err != nil {
			return err
		}
		body := map[string]any{"id": node.NodeID, "region_id": "hakopod", "version": node.Generation, "host": node.Host, "port": 5454, "active": true, "http_port": 7677, "https_port": 7676, "availability_zone_id": node.AvailabilityZone}
		var created []byte
		if ownershipRequired {
			created, err = r.control.doJSONOwned(ctx, controller, http.MethodPost, "/control/v1/safekeeper/"+strconv.FormatInt(node.NodeID, 10), body, intent.ID, http.StatusOK)
		} else {
			created, err = r.control.doJSON(ctx, controller, http.MethodPost, "/control/v1/safekeeper/"+strconv.FormatInt(node.NodeID, 10), body, http.StatusNoContent)
		}
		if err != nil {
			return fmt.Errorf("register Neon safekeeper %s left ownership pending: %w", node.Name, err)
		}
		if ownershipRequired {
			if err = verifyNeonOwnershipToken(created, intent.ID); err != nil {
				return fmt.Errorf("register Neon safekeeper %s returned invalid ownership: %w", node.Name, err)
			}
		}
		observed, err = r.control.doJSON(ctx, controller, http.MethodGet, "/control/v1/safekeeper/"+strconv.FormatInt(node.NodeID, 10), nil, http.StatusOK)
		if err != nil {
			return err
		}
		if err = verifyNeonSafekeeperRegistration(observed, node); err != nil {
			return err
		}
		if ownershipRequired {
			if err = verifyNeonOwnershipToken(observed, intent.ID); err != nil {
				return fmt.Errorf("inspect Neon safekeeper %s returned invalid ownership: %w", node.Name, err)
			}
			identity, err = encodeNeonOwnedResourceID(identity, intent.ID)
			if err != nil {
				return err
			}
		}
		claim, err = r.confirmPending(ctx, pending, intent, identity, node.Generation)
		if err != nil {
			return err
		}
		current[component] = claim
	}
	return nil
}

func verifyNeonPageserverRegistration(body []byte, want NeonPageserverRegistration) error {
	var got struct {
		ID               json.Number `json:"id"`
		AvailabilityZone string      `json:"availability_zone_id"`
		HTTPHost         string      `json:"listen_http_addr"`
		HTTPPort         int         `json:"listen_http_port"`
		HTTPSPort        *int        `json:"listen_https_port"`
		PGHost           string      `json:"listen_pg_addr"`
		PGPort           int         `json:"listen_pg_port"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		return fmt.Errorf("decode Neon pageserver registration: %w", err)
	}
	if got.ID.String() != strconv.FormatInt(want.NodeID, 10) || got.HTTPHost != want.Host || got.HTTPPort != 9897 || got.HTTPSPort == nil || *got.HTTPSPort != 9898 || got.PGHost != want.Host || got.PGPort != 6400 || got.AvailabilityZone != want.AvailabilityZone {
		return fmt.Errorf("Neon pageserver %s registration identity changed", want.Name)
	}
	return nil
}

func verifiedNeonPageserverRegistrationIdentity(body []byte, want NeonPageserverRegistration) (string, error) {
	var observed struct {
		ID               json.Number `json:"id"`
		AvailabilityZone string      `json:"availability_zone_id"`
		HTTPHost         string      `json:"listen_http_addr"`
		HTTPPort         int         `json:"listen_http_port"`
		HTTPSPort        *int        `json:"listen_https_port"`
		PGHost           string      `json:"listen_pg_addr"`
		PGPort           int         `json:"listen_pg_port"`
	}
	if err := json.Unmarshal(body, &observed); err != nil {
		return "", fmt.Errorf("decode Neon pageserver registration: %w", err)
	}
	if observed.AvailabilityZone == "" || len(validation.IsDNS1123Label(observed.AvailabilityZone)) != 0 {
		return "", fmt.Errorf("Neon pageserver %s registration has an invalid availability zone", want.Name)
	}
	want.AvailabilityZone = observed.AvailabilityZone
	if err := verifyNeonPageserverRegistration(body, want); err != nil {
		return "", err
	}
	return neonStorageIdentity("pageserver", want.Name, want.NodeID, want.Generation, want.Host, observed.AvailabilityZone), nil
}

func verifyNeonSafekeeperRegistration(body []byte, want NeonSafekeeperRegistration) error {
	var got struct {
		ID               json.Number `json:"id"`
		Region           string      `json:"region_id"`
		Version          int64       `json:"version"`
		Host             string      `json:"host"`
		Port             int         `json:"port"`
		HTTPPort         int         `json:"http_port"`
		HTTPSPort        *int        `json:"https_port"`
		AvailabilityZone string      `json:"availability_zone_id"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		return fmt.Errorf("decode Neon safekeeper registration: %w", err)
	}
	if got.ID.String() != strconv.FormatInt(want.NodeID, 10) || got.Region != "hakopod" || got.Version != want.Generation || got.Host != want.Host || got.Port != 5454 || got.HTTPPort != 7677 || got.HTTPSPort == nil || *got.HTTPSPort != 7676 || got.AvailabilityZone != want.AvailabilityZone {
		return fmt.Errorf("Neon safekeeper %s registration identity changed", want.Name)
	}
	return nil
}

func verifiedNeonSafekeeperRegistrationIdentity(body []byte, want NeonSafekeeperRegistration) (string, error) {
	var observed struct {
		AvailabilityZone string `json:"availability_zone_id"`
	}
	if err := json.Unmarshal(body, &observed); err != nil {
		return "", fmt.Errorf("decode Neon safekeeper registration: %w", err)
	}
	if observed.AvailabilityZone == "" || len(validation.IsDNS1123Label(observed.AvailabilityZone)) != 0 {
		return "", fmt.Errorf("Neon safekeeper %s registration has an invalid availability zone", want.Name)
	}
	want.AvailabilityZone = observed.AvailabilityZone
	if err := verifyNeonSafekeeperRegistration(body, want); err != nil {
		return "", err
	}
	return neonStorageIdentity("safekeeper", want.Name, want.NodeID, want.Generation, want.Host, observed.AvailabilityZone), nil
}

func neonTenantCreateBody(request NeonLifecycleRequest) map[string]any {
	body := map[string]any{"new_tenant_id": request.TenantID}
	if request.RecoveryTenantGeneration > 0 {
		body["generation"] = request.RecoveryTenantGeneration
	}
	return body
}

func verifiedTenantCreate(body []byte, tenantID string) (string, int64, error) {
	var response struct {
		Shards []struct {
			ShardID    string      `json:"shard_id"`
			NodeID     json.Number `json:"node_id"`
			Generation int64       `json:"generation"`
		} `json:"shards"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return "", 0, fmt.Errorf("decode Neon tenant create response: %w", err)
	}
	if len(response.Shards) != 1 || response.Shards[0].ShardID != tenantID || response.Shards[0].Generation < 1 {
		return "", 0, fmt.Errorf("Neon tenant create response did not contain one matching unsharded attachment")
	}
	node := response.Shards[0].NodeID.String()
	if value, err := strconv.ParseUint(node, 10, 64); err != nil || value == 0 {
		return "", 0, fmt.Errorf("Neon tenant create response contained an invalid node identity")
	}
	return tenantID + "@" + node, response.Shards[0].Generation, nil
}

func verifiedTimelineCreate(body []byte, tenantID, timelineID string) (string, int64, []string, error) {
	var response struct {
		TenantID    string `json:"tenant_id"`
		TimelineID  string `json:"timeline_id"`
		Safekeepers *struct {
			TenantID   string `json:"tenant_id"`
			TimelineID string `json:"timeline_id"`
			Generation int64  `json:"generation"`
			Members    []struct {
				ID       json.Number `json:"id"`
				Hostname string      `json:"hostname"`
			} `json:"safekeepers"`
		} `json:"safekeepers"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return "", 0, nil, fmt.Errorf("decode Neon timeline create response: %w", err)
	}
	if response.TenantID != tenantID || response.TimelineID != timelineID || response.Safekeepers == nil || response.Safekeepers.TenantID != tenantID || response.Safekeepers.TimelineID != timelineID || response.Safekeepers.Generation < 1 {
		return "", 0, nil, fmt.Errorf("Neon timeline response omitted its exact tenant, timeline, or safekeeper generation")
	}
	members := make([]neonTimelineMember, 0, len(response.Safekeepers.Members))
	for _, member := range response.Safekeepers.Members {
		members = append(members, neonTimelineMember{ID: member.ID.String(), Host: member.Hostname})
	}
	return verifiedNeonTimelineIdentity(tenantID, timelineID, response.Safekeepers.Generation, members)
}

type neonTimelineMember struct {
	ID   string
	Host string
}

func verifiedNeonTimelineIdentity(tenantID, timelineID string, generation int64, members []neonTimelineMember) (string, int64, []string, error) {
	if !neonID.MatchString(tenantID) || !neonID.MatchString(timelineID) || generation < 1 {
		return "", 0, nil, fmt.Errorf("Neon timeline identity is invalid")
	}
	identities := make([]string, 0, len(members))
	hosts := make([]string, 0, len(members))
	seenID := map[string]bool{}
	seenHost := map[string]bool{}
	for _, member := range members {
		if value, err := strconv.ParseUint(member.ID, 10, 64); err != nil || value == 0 || member.Host == "" || len(member.Host) > 255 || seenID[member.ID] || seenHost[member.Host] {
			return "", 0, nil, fmt.Errorf("Neon returned invalid safekeeper placement")
		}
		seenID[member.ID] = true
		seenHost[member.Host] = true
		identities = append(identities, member.ID+"@"+member.Host)
		hosts = append(hosts, member.Host)
	}
	if len(identities) < 3 || len(identities) > 8 {
		return "", 0, nil, fmt.Errorf("Neon returned %d safekeepers; expected 3-8", len(identities))
	}
	sort.Strings(identities)
	sort.Strings(hosts)
	sum := sha256.Sum256([]byte(tenantID + "\x00" + timelineID + "\x00" + strings.Join(identities, "\x00")))
	return hex.EncodeToString(sum[:]), generation, hosts, nil
}

func (r *DurableNeonRuntime) inspectTenant(ctx context.Context, tenantID string) (bool, string, int64, string, string, string, error) {
	described, status, err := r.control.request(ctx, r.control.config.StorageController, http.MethodGet, "/control/v1/tenant/"+tenantID, nil)
	if err != nil {
		return false, "", 0, "", "", "", err
	}
	if status == http.StatusNotFound {
		return false, "", 0, "", "", "", nil
	}
	if status != http.StatusOK {
		return false, "", 0, "", "", "", fmt.Errorf("Neon tenant inspection returned HTTP %d", status)
	}
	ownershipToken := ""
	ownershipState := ""
	deletionToken := ""
	if !r.control.config.allowUnqualifiedOwnershipProtocolForTest {
		ownershipToken, err = neonOwnershipToken(described)
		if err != nil {
			return false, "", 0, "", "", "", fmt.Errorf("inspect Neon tenant ownership: %w", err)
		}
		var ownership struct {
			State         string `json:"ownership_state"`
			DeletionToken string `json:"ownership_delete_token"`
		}
		if err = json.Unmarshal(described, &ownership); err != nil || ownership.State != "completed" && ownership.State != "deleting" {
			return false, "", 0, "", "", "", fmt.Errorf("inspect Neon tenant ownership state is missing or invalid")
		}
		ownershipState = ownership.State
		deletionToken = ownership.DeletionToken
		if deletionToken != "" && !validNeonComputeOwnershipToken(deletionToken) {
			return false, "", 0, "", "", "", fmt.Errorf("inspect Neon tenant deletion token is missing or invalid")
		}
		if ownershipState == "deleting" {
			return true, "", 0, ownershipToken, ownershipState, deletionToken, nil
		}
	}
	body, err := r.control.doJSON(ctx, r.control.config.StorageController, http.MethodPost, "/debug/v1/inspect", map[string]string{"tenant_shard_id": tenantID}, http.StatusOK)
	if err != nil {
		return false, "", 0, "", "", "", err
	}
	var response struct {
		Attachment []json.RawMessage `json:"attachment"`
	}
	if err = json.Unmarshal(body, &response); err != nil || len(response.Attachment) != 2 {
		return false, "", 0, "", "", "", fmt.Errorf("Neon tenant inspect omitted the exact attachment identity")
	}
	var generation int64
	var node json.Number
	if err = json.Unmarshal(response.Attachment[0], &generation); err != nil || generation < 1 {
		return false, "", 0, "", "", "", fmt.Errorf("Neon tenant inspect returned an invalid generation")
	}
	if err = json.Unmarshal(response.Attachment[1], &node); err != nil {
		return false, "", 0, "", "", "", fmt.Errorf("Neon tenant inspect returned an invalid node identity")
	}
	if value, parseErr := strconv.ParseUint(node.String(), 10, 64); parseErr != nil || value == 0 {
		return false, "", 0, "", "", "", fmt.Errorf("Neon tenant inspect returned an invalid node identity")
	}
	return true, tenantID + "@" + node.String(), generation, ownershipToken, ownershipState, deletionToken, nil
}

func (r *DurableNeonRuntime) inspectTimeline(ctx context.Context, tenantID, timelineID string) (bool, string, error) {
	body, status, err := r.control.request(ctx, r.control.config.StorageController, http.MethodGet, "/control/v1/tenant/"+tenantID+"/timeline/"+timelineID, nil)
	if err != nil {
		return false, "", err
	}
	if status == http.StatusNotFound {
		return false, "", nil
	}
	if status == http.StatusServiceUnavailable {
		// The controller's detailed endpoint maps a missing pageserver
		// timeline to 503. Its canonical endpoint preserves timeline 404s
		// while converting missing tenants to 503. Only that 404 proves
		// absence; a successful fallback cannot establish ownership.
		_, canonicalStatus, canonicalErr := r.control.request(ctx, r.control.config.StorageController, http.MethodGet, "/v1/tenant/"+tenantID+"/timeline/"+timelineID, nil)
		if canonicalErr == nil && canonicalStatus == http.StatusNotFound {
			return false, "", nil
		}
	}
	if status != http.StatusOK {
		return false, "", fmt.Errorf("Neon timeline inspection returned HTTP %d", status)
	}
	var response struct {
		Shards []struct {
			TenantID   string `json:"tenant_id"`
			TimelineID string `json:"timeline_id"`
		} `json:"shards"`
	}
	if err = json.Unmarshal(body, &response); err != nil || len(response.Shards) != 1 || response.Shards[0].TenantID != tenantID || response.Shards[0].TimelineID != timelineID {
		return false, "", fmt.Errorf("Neon timeline inspection did not return one exact unsharded identity")
	}
	ownershipToken := ""
	if !r.control.config.allowUnqualifiedOwnershipProtocolForTest {
		ownershipToken, err = neonOwnershipToken(body)
		if err != nil {
			return false, "", fmt.Errorf("inspect Neon timeline ownership: %w", err)
		}
	}
	return true, ownershipToken, nil
}

func (r *DurableNeonRuntime) inspectSafekeeperTimeline(ctx context.Context, tenantID, timelineID string) (bool, string, int64, []string, string, error) {
	path := "/v1/tenant/" + tenantID + "/timeline/" + timelineID
	expectedMembers := make(map[string]string, len(r.control.config.Safekeepers))
	for _, node := range r.control.config.Safekeepers {
		expectedMembers[strconv.FormatInt(node.NodeID, 10)] = node.Host
	}
	var expectedExists *bool
	var expectedIdentity string
	var expectedGeneration int64
	var expectedHosts []string
	var expectedOwnershipToken string
	for _, node := range r.control.config.Safekeepers {
		target := NeonControlTarget{
			Name:   "safekeeper-" + node.Name,
			Origin: "https://" + net.JoinHostPort(node.Host, "7676"),
			Token:  r.control.config.SafekeeperToken,
		}
		statusBody, statusCode, err := r.control.request(ctx, target, http.MethodGet, "/v1/status", nil)
		if err != nil || statusCode != http.StatusOK {
			return false, "", 0, nil, "", fmt.Errorf("inspect Neon safekeeper %s identity returned HTTP %d: %w", node.Name, statusCode, err)
		}
		var local struct {
			ID json.Number `json:"id"`
		}
		if err = json.Unmarshal(statusBody, &local); err != nil || local.ID.String() != strconv.FormatInt(node.NodeID, 10) {
			return false, "", 0, nil, "", fmt.Errorf("Neon safekeeper %s node identity changed", node.Name)
		}
		body, status, err := r.control.request(ctx, target, http.MethodGet, path, nil)
		if err != nil {
			return false, "", 0, nil, "", err
		}
		exists := status == http.StatusOK
		if status != http.StatusOK && status != http.StatusNotFound {
			return false, "", 0, nil, "", fmt.Errorf("inspect Neon safekeeper %s timeline returned HTTP %d", node.Name, status)
		}
		if expectedExists != nil && *expectedExists != exists {
			return false, "", 0, nil, "", fmt.Errorf("Neon safekeeper timeline presence is inconsistent")
		}
		if expectedExists == nil {
			value := exists
			expectedExists = &value
		}
		if !exists {
			continue
		}
		var response struct {
			TenantID   string `json:"tenant_id"`
			TimelineID string `json:"timeline_id"`
			Membership struct {
				Generation int64 `json:"generation"`
				Members    []struct {
					ID     json.Number `json:"id"`
					Host   string      `json:"host"`
					PGPort int         `json:"pg_port"`
				} `json:"members"`
				NewMembers *[]struct {
					ID     json.Number `json:"id"`
					Host   string      `json:"host"`
					PGPort int         `json:"pg_port"`
				} `json:"new_members"`
			} `json:"mconf"`
		}
		if err = json.Unmarshal(body, &response); err != nil {
			return false, "", 0, nil, "", fmt.Errorf("decode Neon safekeeper %s timeline: %w", node.Name, err)
		}
		ownershipToken := ""
		if !r.control.config.allowUnqualifiedOwnershipProtocolForTest {
			ownershipToken, err = neonOwnershipToken(body)
			if err != nil {
				return false, "", 0, nil, "", fmt.Errorf("inspect Neon safekeeper %s timeline ownership: %w", node.Name, err)
			}
			if expectedOwnershipToken != "" && ownershipToken != expectedOwnershipToken {
				return false, "", 0, nil, "", fmt.Errorf("Neon safekeeper timeline ownership is inconsistent")
			}
			expectedOwnershipToken = ownershipToken
		}
		if response.TenantID != tenantID || response.TimelineID != timelineID || response.Membership.NewMembers != nil || len(response.Membership.Members) != len(expectedMembers) {
			return false, "", 0, nil, "", fmt.Errorf("Neon safekeeper %s returned an unstable timeline membership", node.Name)
		}
		members := make([]neonTimelineMember, 0, len(response.Membership.Members))
		for _, member := range response.Membership.Members {
			memberID := member.ID.String()
			if expectedMembers[memberID] != member.Host || member.PGPort != 5454 {
				return false, "", 0, nil, "", fmt.Errorf("Neon safekeeper %s returned an unexpected timeline member", node.Name)
			}
			members = append(members, neonTimelineMember{ID: memberID, Host: member.Host})
		}
		identity, generation, hosts, identityErr := verifiedNeonTimelineIdentity(tenantID, timelineID, response.Membership.Generation, members)
		if identityErr != nil {
			return false, "", 0, nil, "", identityErr
		}
		if expectedIdentity != "" && (identity != expectedIdentity || generation != expectedGeneration || !equalNeonStrings(hosts, expectedHosts)) {
			return false, "", 0, nil, "", fmt.Errorf("Neon safekeeper timeline membership is inconsistent")
		}
		expectedIdentity = identity
		expectedGeneration = generation
		expectedHosts = hosts
	}
	if expectedExists == nil {
		return false, "", 0, nil, "", fmt.Errorf("Neon safekeeper inventory is empty")
	}
	return *expectedExists, expectedIdentity, expectedGeneration, expectedHosts, expectedOwnershipToken, nil
}

func (r *DurableNeonRuntime) verifyConfiguredSafekeeperHosts(hosts []string) error {
	expected := make([]string, 0, len(r.control.config.Safekeepers))
	for _, node := range r.control.config.Safekeepers {
		expected = append(expected, node.Host)
	}
	sort.Strings(expected)
	if !equalNeonStrings(expected, hosts) {
		return fmt.Errorf("Neon timeline placement does not match the configured safekeepers")
	}
	return nil
}

func (r *DurableNeonRuntime) configuredSafekeeperIdentity(tenantID, timelineID string, generation int64) (string, error) {
	members := make([]neonTimelineMember, 0, len(r.control.config.Safekeepers))
	for _, node := range r.control.config.Safekeepers {
		members = append(members, neonTimelineMember{ID: strconv.FormatInt(node.NodeID, 10), Host: node.Host})
	}
	identity, _, _, err := verifiedNeonTimelineIdentity(tenantID, timelineID, generation, members)
	return identity, err
}

func equalNeonStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func neonComputeIdentity(target NeonControlTarget) (string, string, error) {
	u, err := url.Parse(target.Origin)
	if err != nil {
		return "", "", err
	}
	u.Scheme = strings.ToLower(u.Scheme)
	u.Host = strings.ToLower(u.Host)
	u.Path = strings.TrimSuffix(u.Path, "/")
	canonical := target.Name + "\x00" + u.String()
	sum := sha256.Sum256([]byte(canonical))
	id := hex.EncodeToString(sum[:])
	return id, id, nil
}

func neonComputeDetached(body []byte) bool {
	var status struct {
		Tenant   string `json:"tenant"`
		Timeline string `json:"timeline"`
		Status   string `json:"status"`
	}
	return json.Unmarshal(body, &status) == nil && status.Status != "running" && status.Tenant == "" && status.Timeline == ""
}
