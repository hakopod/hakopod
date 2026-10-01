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
const neonComputeOwnershipHeader = "Hakopod-Ownership-Token"

// neonOwnershipCapability is returned by the storage controller capability
// endpoint. Provisioning must stay disabled unless all provider mutations are
// covered; partial support would reopen the adoption race.
type neonOwnershipCapability struct {
	Protocol  string   `json:"protocol"`
	Mutations []string `json:"mutations"`
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
	if !validNeonComputeOwnershipToken(token) {
		return "", fmt.Errorf("Neon compute ownership token is invalid")
	}
	return endpointIdentity + ":" + token, nil
}

func parseNeonComputeClaimResourceID(resourceID string) (string, string, error) {
	if len(resourceID) != 97 || resourceID[64] != ':' {
		return "", "", fmt.Errorf("Neon compute ownership claim is malformed")
	}
	endpointIdentity, token := resourceID[:64], resourceID[65:]
	if !validLowerHex(endpointIdentity, 64) || !validNeonComputeOwnershipToken(token) {
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
