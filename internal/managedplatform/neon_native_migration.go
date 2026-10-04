//go:build hakopod_native_acceptance && linux

package managedplatform

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"
)

func (r *DurableNeonRuntime) MigrateTenantNative(ctx context.Context, platformID, tenantID string, source, destination int64, claims []DurableResourceClaim) (int64, int64, error) {
	defer r.closeIdleConnections()
	if r == nil || r.control == nil || source == destination || source < 1 || destination < 1 || source > int64(len(r.control.config.Pageservers)) || destination > int64(len(r.control.config.Pageservers)) {
		return 0, 0, fmt.Errorf("native Neon migration target is invalid")
	}
	byComponent := map[string]DurableResourceClaim{}
	for _, claim := range claims {
		if _, exists := byComponent[claim.Component]; exists {
			return 0, 0, fmt.Errorf("native Neon migration claims are ambiguous")
		}
		byComponent[claim.Component] = claim
	}
	tenant, exists := byComponent["tenant"]
	if !exists {
		return 0, 0, fmt.Errorf("native Neon migration tenant is unowned")
	}
	before, generation, err := ObserveNeonTenantPlacement(ctx, r.control.config.StorageController, r.control.config.RootCAs, platformID, tenantID, len(r.control.config.Pageservers), source, tenant, byComponent["pageserver-registration-"+strconv.FormatInt(source-1, 10)])
	if err != nil || before != tenant.ResourceID || generation != tenant.ImmutableGeneration {
		return 0, 0, fmt.Errorf("native Neon migration source changed")
	}
	destinationClaim, exists := byComponent["pageserver-registration-"+strconv.FormatInt(destination-1, 10)]
	if !exists || destinationClaim.Kind != "runtime_component" || destinationClaim.ImmutableGeneration != 1 {
		return 0, 0, fmt.Errorf("native Neon migration destination is unowned")
	}
	var target NeonPageserverRegistration
	for _, node := range r.control.config.Pageservers {
		if node.NodeID == destination {
			target = node
		}
	}
	body, status, err := r.control.request(ctx, r.control.config.StorageController, http.MethodGet, "/control/v1/node/"+strconv.FormatInt(destination, 10), nil)
	identity, identityErr := verifiedNeonPageserverRegistrationIdentity(body, target)
	owned, token, parseErr := parseNeonOwnedResourceID(destinationClaim.ResourceID)
	var ownership struct {
		State string `json:"ownership_state"`
	}
	if err != nil || status != http.StatusOK || identityErr != nil || parseErr != nil || json.Unmarshal(body, &ownership) != nil || ownership.State != "completed" || owned != identity || verifyNeonOwnershipToken(body, token) != nil {
		return 0, 0, fmt.Errorf("native Neon migration destination ownership changed")
	}
	_, tenantToken, err := parseNeonOwnedResourceID(tenant.ResourceID)
	if err != nil {
		return 0, 0, err
	}
	payload, _ := json.Marshal(map[string]any{"node_id": destination, "origin_node_id": source, "migration_config": map[string]any{"override_scheduler": true, "prewarm": false, "secondary_warmup_timeout": "5s", "secondary_download_request_timeout": "2s"}})
	// This isolated native runtime has not been shared with another goroutine.
	r.control.config.RequestTimeout = 60 * time.Second
	r.control.client.Timeout = 60 * time.Second
	if transport, ok := r.control.client.Transport.(*http.Transport); ok {
		transport.ResponseHeaderTimeout = 60 * time.Second
	}
	_, status, err = r.control.requestOwned(ctx, r.control.config.StorageController, http.MethodPut, "/control/v1/tenant/"+tenantID+"/migrate", payload, tenantToken)
	if err != nil || status != http.StatusOK {
		return 0, 0, fmt.Errorf("native Neon controller migration did not complete")
	}
	_, after, err := ObserveNeonTenantPlacement(ctx, r.control.config.StorageController, r.control.config.RootCAs, platformID, tenantID, len(r.control.config.Pageservers), destination, tenant, destinationClaim)
	if err != nil || after <= generation {
		return 0, 0, fmt.Errorf("native Neon migration did not advance the owned placement")
	}
	return generation, after, nil
}

func (r *DurableNeonRuntime) VerifyNativeComputeRouting(ctx context.Context, request NeonNativeProbeRequest, configs map[string]json.RawMessage) error {
	defer r.closeIdleConnections()
	claims, err := nativeNeonClaims(request.Claims, r.control.config)
	if err != nil {
		return err
	}
	if len(configs) != len(r.control.config.Computes) {
		return fmt.Errorf("native Neon routing inventory is incomplete")
	}
	for _, target := range r.control.config.Computes {
		_, token, err := parseNeonComputeClaimResourceID(claims["compute-"+target.Name].ResourceID)
		if err != nil {
			return err
		}
		routing, err := neonComputeRoutingDigest(configs[target.Name])
		if err != nil {
			return err
		}
		body, status, err := r.control.request(ctx, target, http.MethodGet, "/status", nil)
		if err != nil || status != http.StatusOK {
			return fmt.Errorf("native Neon compute routing is unavailable")
		}
		if _, err = verifyNeonComputeDigests(body, token, request.TenantID, request.TimelineID, routing); err != nil {
			return fmt.Errorf("native Neon compute routing did not follow the tenant")
		}
	}
	return nil
}
