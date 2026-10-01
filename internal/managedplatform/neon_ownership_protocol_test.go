package managedplatform

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestNeonOwnershipCapabilityRequiresEveryMutation(t *testing.T) {
	complete := []byte(`{"protocol":"hakopod-ownership-v1","mutations":["tenant","timeline","pageserver_registration","safekeeper_registration"]}`)
	if err := verifyNeonOwnershipCapability(complete); err != nil {
		t.Fatal(err)
	}
	partial := []byte(`{"protocol":"hakopod-ownership-v1","mutations":["tenant","timeline"]}`)
	if err := verifyNeonOwnershipCapability(partial); err == nil {
		t.Fatal("partial provider ownership support enabled creation")
	}
}

func TestBindNeonComputeOwnershipRejectsConflictingToken(t *testing.T) {
	raw := json.RawMessage(`{"spec":{"operation_uuid":"44444444444444444444444444444444"},"compute_ctl_config":{}}`)
	if _, err := bindNeonComputeOwnership(raw, "33333333333333333333333333333333"); err == nil {
		t.Fatal("conflicting compute ownership token was replaced")
	}
}

func TestBindAndVerifyNeonComputeOwnership(t *testing.T) {
	const owned = "33333333333333333333333333333333"
	const foreign = "44444444444444444444444444444444"
	raw, err := bindNeonComputeOwnership(json.RawMessage(`{"spec":{},"compute_ctl_config":{}}`), owned)
	if err != nil {
		t.Fatal(err)
	}
	var root map[string]any
	if err = json.Unmarshal(raw, &root); err != nil {
		t.Fatal(err)
	}
	if root["spec"].(map[string]any)["operation_uuid"] != owned {
		t.Fatal("compute token was not bound")
	}
	if err = verifyNeonComputeOwnershipStatus([]byte(`{"operation_uuid":"`+owned+`"}`), owned); err != nil {
		t.Fatal(err)
	}
	if err = verifyNeonComputeOwnershipStatus([]byte(`{"operation_uuid":"`+foreign+`"}`), owned); err == nil {
		t.Fatal("foreign compute token was accepted")
	}
}

func TestNeonComputeClaimResourceIDPreservesStableOwnershipToken(t *testing.T) {
	endpoint := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	token := "33333333333333333333333333333333"
	resourceID, err := encodeNeonComputeClaimResourceID(endpoint, token)
	if err != nil {
		t.Fatal(err)
	}
	parsedEndpoint, parsedToken, err := parseNeonComputeClaimResourceID(resourceID)
	if err != nil || parsedEndpoint != endpoint || parsedToken != token {
		t.Fatalf("compute claim did not preserve endpoint and token: %q %q %v", parsedEndpoint, parsedToken, err)
	}
	for _, malformed := range []string{endpoint, endpoint + ":owned", strings.ToUpper(endpoint) + ":" + token, endpoint + ":" + strings.Repeat("0", 32)} {
		if _, _, err = parseNeonComputeClaimResourceID(malformed); err == nil {
			t.Fatalf("malformed compute claim was accepted: %q", malformed)
		}
	}
}

func TestNeonOwnedResourceIDAndDescribeTokenStayBound(t *testing.T) {
	identity := "11111111111111111111111111111111@7"
	token := "33333333333333333333333333333333"
	resourceID, err := encodeNeonOwnedResourceID(identity, token)
	if err != nil {
		t.Fatal(err)
	}
	parsedIdentity, parsedToken, err := parseNeonOwnedResourceID(resourceID)
	if err != nil || parsedIdentity != identity || parsedToken != token {
		t.Fatalf("owned resource claim did not preserve identity and token: %q %q %v", parsedIdentity, parsedToken, err)
	}
	if err = verifyNeonOwnershipToken([]byte(`{"ownership_token":"`+token+`"}`), token); err != nil {
		t.Fatal(err)
	}
	if err = verifyNeonOwnershipToken([]byte(`{"ownership_token":"44444444444444444444444444444444"}`), token); err == nil {
		t.Fatal("foreign provider ownership token was accepted")
	}
	for _, malformed := range []string{"", identity, identity + ":" + strings.Repeat("0", 32), identity + ":short"} {
		if _, _, err = parseNeonOwnedResourceID(malformed); err == nil {
			t.Fatalf("malformed owned resource claim was accepted: %q", malformed)
		}
	}
}

func TestNeonOwnedRequestHashBindsKindKeyAndParameters(t *testing.T) {
	a, err := neonOwnedRequestHash("tenant", "key", []byte(`{"a":1,"b":2}`))
	if err != nil {
		t.Fatal(err)
	}
	b, err := neonOwnedRequestHash("tenant", "key", []byte(`{"b":2,"a":1}`))
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Fatal("equivalent JSON did not canonicalize")
	}
	c, _ := neonOwnedRequestHash("tenant", "other", []byte(`{"a":1,"b":2}`))
	if a == c {
		t.Fatal("external key was not bound")
	}
}
