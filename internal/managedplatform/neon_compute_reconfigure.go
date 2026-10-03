package managedplatform

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
)

type neonComputeDigests struct {
	Status        string `json:"status"`
	OperationUUID string `json:"operation_uuid"`
	Spec          string `json:"spec_sha256"`
	Authorized    string `json:"authorized_spec_sha256"`
	Routing       string `json:"routing_sha256"`
}

// The provider normalizes its full ComputeSpec before hashing. Echo that hash
// for CAS and compare only this explicit shared routing projection locally.
func neonComputeRoutingDigest(raw json.RawMessage) (string, error) {
	var value struct {
		Spec struct {
			Pageserver  *string  `json:"pageserver_connstring"`
			Safekeepers []string `json:"safekeeper_connstrings"`
			Generation  *uint32  `json:"safekeepers_generation"`
			Tenant      *string  `json:"tenant_id"`
			Timeline    *string  `json:"timeline_id"`
		} `json:"spec"`
	}
	if len(raw) > maxNeonResponseBytes || json.Unmarshal(raw, &value) != nil || value.Spec.Pageserver == nil || value.Spec.Generation == nil || value.Spec.Tenant == nil || value.Spec.Timeline == nil || !neonID.MatchString(*value.Spec.Tenant) || !neonID.MatchString(*value.Spec.Timeline) || len(value.Spec.Safekeepers) != 3 {
		return "", fmt.Errorf("Neon compute routing is invalid")
	}
	sort.Strings(value.Spec.Safekeepers)
	projection := map[string]any{"pageserver_connstring": value.Spec.Pageserver, "safekeeper_connstrings": value.Spec.Safekeepers, "safekeepers_generation": value.Spec.Generation, "tenant_id": value.Spec.Tenant, "timeline_id": value.Spec.Timeline}
	var canonical bytes.Buffer
	encoder := json.NewEncoder(&canonical)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(projection); err != nil {
		return "", err
	}
	digest := sha256.Sum256(bytes.TrimSuffix(canonical.Bytes(), []byte{'\n'}))
	return hex.EncodeToString(digest[:]), nil
}

func verifyNeonComputeDigests(body []byte, token, tenantID, timelineID, routing string) (neonComputeDigests, error) {
	var digest neonComputeDigests
	if json.Unmarshal(body, &digest) != nil || verifyNeonComputeOwnershipStatus(body, token) != nil || verifyComputeStatus(body, tenantID, timelineID) != nil || !validLowerHex(digest.Spec, 64) || digest.Spec != digest.Authorized || digest.Routing != routing {
		return digest, fmt.Errorf("Neon compute configuration is not observed")
	}
	return digest, nil
}

// configureOwnedCompute is safe to retry after a client deadline. A configure
// continues asynchronously in compute_ctl; the next pass observes its applied
// routing before deciding whether another configure is needed.
func (r *NeonRuntime) configureOwnedCompute(ctx context.Context, target NeonControlTarget, configuration json.RawMessage, token, tenantID, timelineID string, initial []byte, allowUnclaimedEmpty bool) error {
	routing, err := neonComputeRoutingDigest(configuration)
	if err != nil {
		return err
	}
	var observed neonComputeDigests
	if json.Unmarshal(initial, &observed) != nil {
		return fmt.Errorf("Neon compute status is invalid")
	}
	expected := ""
	switch observed.Status {
	case "running":
		if verifyNeonComputeOwnershipStatus(initial, token) != nil || verifyComputeStatus(initial, tenantID, timelineID) != nil || !validLowerHex(observed.Spec, 64) || observed.Spec != observed.Authorized || !validLowerHex(observed.Routing, 64) {
			return fmt.Errorf("Neon compute observed ownership or digest changed")
		}
		if observed.Routing == routing {
			return nil
		}
		expected = observed.Spec
	case "empty":
		if observed.Spec != "" || observed.Routing != "" {
			return fmt.Errorf("Neon empty compute reports an applied configuration")
		}
		if observed.OperationUUID != "" {
			if observed.OperationUUID != token || !validLowerHex(observed.Authorized, 64) {
				return fmt.Errorf("Neon empty compute ownership changed")
			}
			expected = observed.Authorized
		} else if !allowUnclaimedEmpty || observed.Authorized != "" {
			return fmt.Errorf("Neon compute durable ownership disappeared")
		}
	default:
		return fmt.Errorf("Neon compute configuration is pending")
	}
	response, status, err := r.requestWithSpecCAS(ctx, target, http.MethodPost, "/configure", configuration, token, "", expected)
	if err != nil || status != http.StatusOK {
		return fmt.Errorf("Neon compute configuration is pending")
	}
	applied, err := verifyNeonComputeDigests(response, token, tenantID, timelineID, routing)
	if err != nil {
		return err
	}
	body, status, err := r.request(ctx, target, http.MethodGet, "/status", nil)
	if err != nil || status != http.StatusOK {
		return fmt.Errorf("Neon compute configuration status is unavailable")
	}
	confirmed, err := verifyNeonComputeDigests(body, token, tenantID, timelineID, routing)
	if err != nil || confirmed.Spec != applied.Spec {
		return fmt.Errorf("Neon compute configuration changed before observation")
	}
	return nil
}
