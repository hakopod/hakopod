package managedplatform

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// ValidateNeonTenantPlacement permits only monotonic attachment changes for
// the same tenant and immutable provider ownership token.
func ValidateNeonTenantPlacement(priorID string, priorGeneration int64, observedID string, generation int64) error {
	prior, priorToken, err := parseNeonOwnedResourceID(priorID)
	observed, token, observedErr := parseNeonOwnedResourceID(observedID)
	if err != nil || observedErr != nil || token != priorToken || priorGeneration < 1 || generation < priorGeneration || generation > 4294967295 {
		return fmt.Errorf("Neon tenant placement ownership or generation changed")
	}
	parse := func(identity string) (string, error) {
		parts := strings.Split(identity, "@")
		if len(parts) != 2 || !neonID.MatchString(parts[0]) {
			return "", fmt.Errorf("invalid Neon tenant placement")
		}
		node, err := strconv.ParseInt(parts[1], 10, 64)
		if err != nil || node < 1 || strconv.FormatInt(node, 10) != parts[1] {
			return "", fmt.Errorf("invalid Neon tenant node")
		}
		return parts[0], nil
	}
	tenant, err := parse(prior)
	observedTenant, observedErr := parse(observed)
	if err != nil || observedErr != nil || tenant != observedTenant || prior != observed && generation == priorGeneration {
		return fmt.Errorf("Neon tenant placement identity changed without a newer generation")
	}
	return nil
}

// ObserveNeonTenantPlacement checks authenticated provider state while its
// callback is pending. These read endpoints do not acquire reconciliation locks.
func ObserveNeonTenantPlacement(ctx context.Context, controller NeonControlTarget, roots *x509.CertPool, platformID, tenantID string, pageservers int, nodeID int64, prior, registration DurableResourceClaim) (string, int64, error) {
	if roots == nil || !neonID.MatchString(platformID) || !neonID.MatchString(tenantID) || pageservers < 2 || pageservers > 8 || nodeID < 1 || nodeID > int64(pageservers) || prior.Component != "tenant" || prior.Kind != "neon_tenant" || registration.Component != "pageserver-registration-"+strconv.FormatInt(nodeID-1, 10) || registration.Kind != "runtime_component" || registration.ImmutableGeneration != 1 {
		return "", 0, fmt.Errorf("invalid Neon tenant placement inventory")
	}
	if err := validateNeonTarget(controller, "storage controller"); err != nil {
		return "", 0, err
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	transport := &http.Transport{Proxy: nil, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots}, MaxConnsPerHost: 1, MaxIdleConns: 1, TLSHandshakeTimeout: time.Second, ResponseHeaderTimeout: time.Second, MaxResponseHeaderBytes: 32 << 10}
	defer transport.CloseIdleConnections()
	control := &NeonRuntime{config: NeonRuntimeConfig{StorageController: controller, RequestTimeout: 2 * time.Second}, client: &http.Client{Transport: transport, Timeout: 2 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return fmt.Errorf("Neon redirects are not permitted") }}}
	body, status, err := control.request(ctx, controller, http.MethodGet, "/control/v1/tenant/"+tenantID+"/placement", nil)
	var placement struct {
		SchemaVersion int    `json:"schema_version"`
		TenantID      string `json:"tenant_id"`
		NodeID        int64  `json:"node_id"`
		Generation    int64  `json:"generation"`
		Token         string `json:"ownership_token"`
		State         string `json:"ownership_state"`
	}
	if err != nil || status != http.StatusOK || json.Unmarshal(body, &placement) != nil || placement.SchemaVersion != 1 || placement.TenantID != tenantID || placement.NodeID != nodeID || placement.State != "completed" {
		return "", 0, fmt.Errorf("Neon tenant placement is not confirmed by the controller")
	}
	generation := placement.Generation
	observed, err := encodeNeonOwnedResourceID(tenantID+"@"+strconv.FormatInt(nodeID, 10), placement.Token)
	if err != nil || ValidateNeonTenantPlacement(prior.ResourceID, prior.ImmutableGeneration, observed, generation) != nil {
		return "", 0, fmt.Errorf("Neon tenant placement ownership or generation changed")
	}
	body, status, err = control.request(ctx, controller, http.MethodGet, "/control/v1/node/"+strconv.FormatInt(nodeID, 10), nil)
	name := strconv.FormatInt(nodeID-1, 10)
	want := NeonPageserverRegistration{Name: name, NodeID: nodeID, Generation: 1, Host: "neon-pageserver-" + name + ".managed-platform-" + platformID + ".svc"}
	liveIdentity, identityErr := verifiedNeonPageserverRegistrationIdentity(body, want)
	var ownership struct {
		State string `json:"ownership_state"`
	}
	ownershipErr := json.Unmarshal(body, &ownership)
	claimedIdentity, claimedToken, claimErr := parseNeonOwnedResourceID(registration.ResourceID)
	if err != nil || status != http.StatusOK || identityErr != nil || claimErr != nil || ownershipErr != nil || ownership.State != "completed" || liveIdentity != claimedIdentity || verifyNeonOwnershipToken(body, claimedToken) != nil {
		return "", 0, fmt.Errorf("Neon tenant placement references an unowned pageserver")
	}
	return observed, generation, nil
}
