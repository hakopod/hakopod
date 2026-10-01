package managedplatform

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
)

const neonOwnershipProtocolV1 = "hakopod-ownership-v1"
const neonOwnershipHeader = "Hakopod-Ownership-Token"
const neonDeletionHeader = "Hakopod-Deletion-Token"

// neonOwnershipCapability is returned by the storage controller capability
// endpoint. Provisioning must stay disabled unless all provider mutations are
// covered; partial support would reopen the adoption race.
type neonOwnershipCapability struct {
	Protocol             string   `json:"protocol"`
	TenantDeleteProtocol string   `json:"tenant_delete_protocol"`
	Mutations            []string `json:"mutations"`
}

func verifyNeonOwnershipCapability(body []byte) error {
	var got neonOwnershipCapability
	if err := json.Unmarshal(body, &got); err != nil {
		return fmt.Errorf("decode Neon ownership capability: %w", err)
	}
	required := map[string]bool{"tenant": false, "timeline": false, "pageserver_registration": false, "safekeeper_registration": false}
	if got.Protocol != neonOwnershipProtocolV1 {
		return fmt.Errorf("Neon provider ownership protocol is unavailable")
	}
	if got.TenantDeleteProtocol != "prepare-v1" {
		return fmt.Errorf("Neon provider ownership protocol omits prepared tenant deletion")
	}
	for _, mutation := range got.Mutations {
		if _, ok := required[mutation]; ok {
			required[mutation] = true
		}
	}
	for mutation, present := range required {
		if !present {
			return fmt.Errorf("Neon provider ownership protocol omits %s", mutation)
		}
	}
	return nil
}

func neonOwnershipToken(body []byte) (string, error) {
	var response struct {
		Token string `json:"ownership_token"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return "", fmt.Errorf("decode Neon ownership identity: %w", err)
	}
	if !validNeonComputeOwnershipToken(response.Token) {
		return "", fmt.Errorf("Neon ownership identity is missing or invalid")
	}
	return response.Token, nil
}

func verifyNeonOwnershipToken(body []byte, expected string) error {
	if !validNeonComputeOwnershipToken(expected) {
		return fmt.Errorf("expected Neon ownership token is invalid")
	}
	observed, err := neonOwnershipToken(body)
	if err != nil {
		return err
	}
	if observed != expected {
		return fmt.Errorf("Neon ownership token does not match")
	}
	return nil
}

// encodeNeonOwnedResourceID keeps the provider identity and its immutable
// ownership token together across Hakopod operation revisions.
func encodeNeonOwnedResourceID(identity, token string) (string, error) {
	if identity == "" || len(identity) > 222 || strings.Contains(identity, ":") {
		return "", fmt.Errorf("Neon provider identity is invalid")
	}
	if !validNeonComputeOwnershipToken(token) {
		return "", fmt.Errorf("Neon ownership token is invalid")
	}
	return identity + ":" + token, nil
}

func parseNeonOwnedResourceID(resourceID string) (string, string, error) {
	separator := strings.LastIndexByte(resourceID, ':')
	if separator < 1 || separator != len(resourceID)-33 {
		return "", "", fmt.Errorf("Neon ownership claim is malformed")
	}
	identity, token := resourceID[:separator], resourceID[separator+1:]
	if len(identity) > 222 || strings.Contains(identity, ":") || !validNeonComputeOwnershipToken(token) {
		return "", "", fmt.Errorf("Neon ownership claim is malformed")
	}
	return identity, token, nil
}

// neonOwnedRequestHash binds a provider token to the exact mutation. The
// provider independently computes the same hash from its decoded request.
func neonOwnedRequestHash(kind, externalKey string, body []byte) (string, error) {
	var value any
	if err := json.Unmarshal(body, &value); err != nil {
		return "", err
	}
	canonical, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(bytes.Join([][]byte{[]byte(kind), []byte(externalKey), canonical}, []byte{0}))
	return hex.EncodeToString(sum[:]), nil
}

func bindNeonComputeOwnership(raw json.RawMessage, token string) (json.RawMessage, error) {
	if !validNeonComputeOwnershipToken(token) {
		return nil, fmt.Errorf("Neon compute ownership token is invalid")
	}
	var root map[string]any
	if err := json.Unmarshal(raw, &root); err != nil {
		return nil, err
	}
	spec, ok := root["spec"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("Neon compute configuration requires a spec object")
	}
	if existing, exists := spec["operation_uuid"]; exists && existing != nil && existing != token {
		return nil, fmt.Errorf("Neon compute configuration has a conflicting ownership token")
	}
	spec["operation_uuid"] = token
	return json.Marshal(root)
}

func verifyNeonComputeOwnershipStatus(body []byte, token string) error {
	if !validNeonComputeOwnershipToken(token) {
		return fmt.Errorf("Neon compute ownership token is invalid")
	}
	var status struct {
		OperationUUID string `json:"operation_uuid"`
	}
	if err := json.Unmarshal(body, &status); err != nil {
		return err
	}
	if status.OperationUUID != token {
		return fmt.Errorf("Neon compute ownership token does not match")
	}
	return nil
}

func encodeNeonComputeClaimResourceID(endpointIdentity, token string) (string, error) {
	if !validLowerHex(endpointIdentity, 64) {
		return "", fmt.Errorf("Neon compute endpoint identity is invalid")
	}
	return encodeNeonOwnedResourceID(endpointIdentity, token)
}

func parseNeonComputeClaimResourceID(resourceID string) (string, string, error) {
	endpointIdentity, token, err := parseNeonOwnedResourceID(resourceID)
	if err != nil || !validLowerHex(endpointIdentity, 64) {
		return "", "", fmt.Errorf("Neon compute ownership claim is malformed")
	}
	return endpointIdentity, token, nil
}

func validNeonComputeOwnershipToken(token string) bool {
	return token != strings.Repeat("0", 32) && validLowerHex(token, 32)
}

func validLowerHex(value string, length int) bool {
	if len(value) != length {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && hex.EncodeToString(decoded) == value
}
