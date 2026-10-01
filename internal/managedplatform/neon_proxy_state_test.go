package managedplatform

import (
	"bytes"
	"reflect"
	"testing"
)

func TestNeonProxyRolesAreEncryptedAndBoundToRouteIdentity(t *testing.T) {
	key := bytes.Repeat([]byte{7}, 32)
	roles := map[string]NeonProxyRoleState{
		"app": {
			SCRAMSecret:            "SCRAM-SHA-256$4096:c2FsdA==$c3RvcmVk:c2VydmVy",
			AllowedIPs:             []string{"10.0.0.0/8"},
			AllowedVPCEndpointIDs:  []string{"vpce-123"},
			BlockPublicConnections: true,
		},
	}
	sealed, err := SealNeonProxyRoles(key, "11111111111111111111111111111111", 4, "11111111111111111111111111111111", roles)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(sealed, []byte(roles["app"].SCRAMSecret)) {
		t.Fatal("sealed proxy role state contains the SCRAM verifier")
	}
	opened, err := OpenNeonProxyRoles(key, "11111111111111111111111111111111", 4, "11111111111111111111111111111111", sealed)
	if err != nil || !reflect.DeepEqual(opened, roles) {
		t.Fatal("sealed proxy role state did not round trip", err)
	}
	for _, changed := range []struct {
		platform string
		revision int64
		endpoint string
	}{
		{platform: "22222222222222222222222222222222", revision: 4, endpoint: "11111111111111111111111111111111"},
		{platform: "11111111111111111111111111111111", revision: 5, endpoint: "11111111111111111111111111111111"},
		{platform: "11111111111111111111111111111111", revision: 4, endpoint: "22222222222222222222222222222222"},
	} {
		if _, err = OpenNeonProxyRoles(key, changed.platform, changed.revision, changed.endpoint, sealed); err == nil {
			t.Fatal("proxy role state opened after its authenticated identity changed")
		}
	}
}
