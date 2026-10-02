//go:build hakopod_native_acceptance && linux

package managedplatform

import (
	"context"
	"fmt"
	"net/http"
	"sort"
)

// NeonNativeProbeResult contains only provider facts which were observed by
// the read-only native acceptance probe. It deliberately excludes endpoints,
// bearer credentials, ownership tokens, and response bodies.
type NeonNativeProbeResult struct {
	SchemaVersion               int      `json:"schema_version"`
	TenantID                    string   `json:"tenant_id"`
	TimelineID                  string   `json:"timeline_id"`
	TenantGeneration            int64    `json:"tenant_generation"`
	TimelineGeneration          int64    `json:"timeline_generation"`
	ComputeNames                []string `json:"compute_names"`
	OwnershipCapabilityVerified bool     `json:"ownership_capability_verified"`
	TLSVerifiedServices         []string `json:"tls_verified_services"`
}

// NeonNativeProbeRequest binds the provider observations to the accepted
// durable claims. Claims must come from the exact accepted platform revision.
type NeonNativeProbeRequest struct {
	TenantID   string
	TimelineID string
	Claims     []DurableResourceClaim
}

// ProbeNeonNative observes an already-created Neon runtime. It has no durable
// lifecycle handle and cannot reserve, confirm, advance, release, or delete a
// provider resource.
func (inspector *DurableNeonRuntime) ProbeNeonNative(ctx context.Context, request NeonNativeProbeRequest) (NeonNativeProbeResult, error) {
	var result NeonNativeProbeResult
	if !neonID.MatchString(request.TenantID) || !neonID.MatchString(request.TimelineID) {
		return result, fmt.Errorf("native Neon probe identity is invalid")
	}
	config := inspector.control.config
	var err error
	claims, err := nativeNeonClaims(request.Claims, config)
	if err != nil {
		return result, err
	}
	result = NeonNativeProbeResult{SchemaVersion: 1, TenantID: request.TenantID, TimelineID: request.TimelineID, ComputeNames: []string{}, TLSVerifiedServices: []string{}}
	if err = inspector.control.verifyOwnershipCapability(ctx); err != nil {
		return NeonNativeProbeResult{}, fmt.Errorf("native Neon ownership capability was not verified")
	}
	result.OwnershipCapabilityVerified = true

	tenantExists, tenantIdentity, tenantGeneration, tenantToken, tenantState, _, err := inspector.inspectTenant(ctx, request.TenantID)
	if err != nil || !tenantExists || tenantState != "completed" {
		return NeonNativeProbeResult{}, fmt.Errorf("native Neon tenant observation failed")
	}
	expectedTenant, expectedTenantToken, err := parseNeonOwnedResourceID(claims["tenant"].ResourceID)
	if err != nil || expectedTenant != tenantIdentity || expectedTenantToken != tenantToken || claims["tenant"].ImmutableGeneration != tenantGeneration {
		return NeonNativeProbeResult{}, fmt.Errorf("native Neon tenant identity does not match the accepted claim")
	}
	result.TenantGeneration = tenantGeneration
	result.TLSVerifiedServices = append(result.TLSVerifiedServices, "storage-controller")

	timelineExists, timelineToken, err := inspector.inspectTimeline(ctx, request.TenantID, request.TimelineID)
	if err != nil || !timelineExists {
		return NeonNativeProbeResult{}, fmt.Errorf("native Neon timeline observation failed")
	}
	safekeeperExists, timelineIdentity, timelineGeneration, _, safekeeperToken, err := inspector.inspectSafekeeperTimeline(ctx, request.TenantID, request.TimelineID)
	if err != nil || !safekeeperExists || timelineToken != safekeeperToken {
		return NeonNativeProbeResult{}, fmt.Errorf("native Neon safekeeper timeline observation failed")
	}
	expectedTimeline, expectedTimelineToken, err := parseNeonOwnedResourceID(claims["timeline"].ResourceID)
	if err != nil || expectedTimeline != timelineIdentity || expectedTimelineToken != timelineToken || claims["timeline"].ImmutableGeneration != timelineGeneration {
		return NeonNativeProbeResult{}, fmt.Errorf("native Neon timeline identity does not match the accepted claim")
	}
	result.TimelineGeneration = timelineGeneration
	result.TLSVerifiedServices = append(result.TLSVerifiedServices, "safekeeper")

	for _, target := range config.Computes {
		claim := claims["compute-"+target.Name]
		expectedEndpoint, token, parseErr := parseNeonComputeClaimResourceID(claim.ResourceID)
		_, endpoint, identityErr := neonComputeIdentity(target)
		body, status, requestErr := inspector.control.request(ctx, target, http.MethodGet, "/status", nil)
		if parseErr != nil || identityErr != nil || expectedEndpoint != endpoint || claim.ImmutableGeneration != 1 || requestErr != nil || status != http.StatusOK || verifyComputeStatus(body, request.TenantID, request.TimelineID) != nil || verifyNeonComputeOwnershipStatus(body, token) != nil {
			return NeonNativeProbeResult{}, fmt.Errorf("native Neon compute %s observation failed", target.Name)
		}
		result.ComputeNames = append(result.ComputeNames, target.Name)
	}
	if len(result.ComputeNames) > 0 {
		result.TLSVerifiedServices = append(result.TLSVerifiedServices, "compute")
	}
	sort.Strings(result.ComputeNames)
	sort.Strings(result.TLSVerifiedServices)
	return result, nil
}

func nativeNeonClaims(values []DurableResourceClaim, config NeonRuntimeConfig) (map[string]DurableResourceClaim, error) {
	wanted := map[string]bool{"tenant": true, "timeline": true}
	for _, target := range config.Computes {
		wanted["compute-"+target.Name] = true
	}
	out := make(map[string]DurableResourceClaim, len(wanted))
	for _, claim := range values {
		if !wanted[claim.Component] {
			continue
		}
		if claim.ResourceID == "" || claim.ImmutableGeneration < 1 || out[claim.Component].Component != "" {
			return nil, fmt.Errorf("native Neon accepted ownership claims are invalid")
		}
		out[claim.Component] = claim
	}
	if len(out) != len(wanted) {
		return nil, fmt.Errorf("native Neon accepted ownership claims are incomplete")
	}
	return out, nil
}
