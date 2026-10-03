package managedplatform

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func neonTestSCRAMVerifier(value byte) string {
	key := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{value}, 32))
	return "SCRAM-SHA-256$4096:c2FsdA==$" + key + ":" + key
}

func TestNeonSQLAuthenticationBindsProxyRolesWithoutChangingTemplate(t *testing.T) {
	raw := json.RawMessage(`{"spec":{"suspend_timeout_seconds":9223372036854775807,"cluster":{"roles":[{"name":"app","encrypted_password":"old","options":[{"name":"CONNECTION LIMIT","value":"12","vartype":"integer"},{"name":"NOLOGIN","vartype":"string"},{"name":"PASSWORD","value":"old","vartype":"string"}]},{"name":"internal","options":[]},{"name":"cloud_admin","options":[]}]}}}`)
	original := append([]byte(nil), raw...)
	roles := map[string]NeonProxyRoleState{"app": {SCRAMSecret: neonTestSCRAMVerifier(1)}, "cloud_admin": {SCRAMSecret: neonTestSCRAMVerifier(2)}}
	bound, err := BindNeonSQLAuthentication(raw, roles)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(raw, original) || !bytes.Contains(bound, []byte("9223372036854775807")) {
		t.Fatal("binding changed the shared template or provider integer")
	}
	var root map[string]any
	if err := decodeNeonJSON(bound, &root); err != nil {
		t.Fatal(err)
	}
	actual := root["spec"].(map[string]any)["cluster"].(map[string]any)["roles"].([]any)
	if len(actual) != 3 || actual[0].(map[string]any)["encrypted_password"] != roles["app"].SCRAMSecret || actual[2].(map[string]any)["encrypted_password"] != roles["cloud_admin"].SCRAMSecret {
		t.Fatal("compute role verifiers differ from the accepted proxy")
	}
	options := actual[0].(map[string]any)["options"].([]any)
	if len(options) != 1 || options[0].(map[string]any)["name"] != "CONNECTION LIMIT" || options[0].(map[string]any)["value"] != "12" {
		t.Fatal("binding changed non-authentication role options")
	}
	if _, changed := actual[1].(map[string]any)["encrypted_password"]; changed {
		t.Fatal("binding changed a role outside the accepted proxy inventory")
	}
	again, err := BindNeonSQLAuthentication(bound, roles)
	if err != nil || !bytes.Equal(bound, again) {
		t.Fatal("replaying authentication binding changed the compute spec")
	}
	otherRoles := map[string]NeonProxyRoleState{"app": {SCRAMSecret: neonTestSCRAMVerifier(3)}}
	other, err := BindNeonSQLAuthentication(original, otherRoles)
	if err != nil || bytes.Contains(other, []byte(roles["app"].SCRAMSecret)) || !bytes.Contains(other, []byte(otherRoles["app"].SCRAMSecret)) {
		t.Fatal("a second platform inherited another platform's verifier")
	}
}

func TestNeonSQLAuthenticationRejectsMalformedCredentialsAndDuplicateRoles(t *testing.T) {
	valid := neonTestSCRAMVerifier(1)
	for _, invalid := range []string{
		"plain-password", "md5" + strings.Repeat("a", 32),
		"SCRAM-SHA-256$4096:c2FsdA==$c3RvcmVk:c2VydmVy",
		strings.Replace(valid, "$4096:", "$04096:", 1),
		strings.Replace(valid, "c2FsdA==", "c2FsdA", 1),
		strings.Replace(valid, "c2FsdA==", "", 1),
		valid + "'", valid + "\n", strings.Repeat("a", 4097),
	} {
		if ValidateNeonSQLRoleCredential("app", invalid) == nil {
			t.Fatal("malformed SCRAM verifier was accepted")
		}
	}
	for _, name := range []string{"", "cloud admin", "app'", strings.Repeat("a", 64)} {
		if ValidateNeonSQLRoleCredential(name, valid) == nil {
			t.Fatal("invalid SQL role name was accepted")
		}
	}
	roles := map[string]NeonProxyRoleState{"app": {SCRAMSecret: valid}}
	before := map[string]NeonProxyRoleState{"app": {SCRAMSecret: valid}}
	for _, raw := range []string{`{}`, `{"spec":{"cluster":{"roles":null}}}`, `{"spec":{"cluster":{"roles":[]}}}`, `{"spec":{"cluster":{"roles":[{"name":"other"}]}}}`, `{"spec":{"cluster":{"roles":[{"name":"app"},{"name":"app"}]}}}`} {
		if _, err := BindNeonSQLAuthentication(json.RawMessage(raw), roles); err == nil {
			t.Fatal("invalid compute role inventory was accepted")
		}
	}
	if !reflect.DeepEqual(roles, before) {
		t.Fatal("rejected authentication binding changed proxy credentials")
	}
}
