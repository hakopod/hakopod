package cluster

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/hakopod/hakopod/internal/managedplatform"
)

const validNeonComputeTemplate = `{"spec":{"format_version":1,"suspend_timeout_seconds":-1,"cluster":{"roles":[],"databases":[],"settings":[]}},"compute_ctl_config":{"jwks":{"keys":[]}}}`

func TestNeonAuthenticationSnapshotKeepsUserConfigurationAndNoSigner(t *testing.T) {
	request := NeonRuntimeRequest{Render: managedplatform.NeonRenderInput{PlatformID: strings.Repeat("a", 32), Spec: managedplatform.Spec{Kind: "neon", Secrets: map[string]managedplatform.SecretReference{}}}, SecretSnapshots: map[string]map[string][]byte{}}
	const sqlVerifier = "SCRAM-SHA-256$4096:c2FsdA==$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="
	request.ProxyEndpoint.Roles = map[string]managedplatform.NeonProxyRoleState{"cloud_admin": {SCRAMSecret: sqlVerifier}}
	for _, logical := range []string{"controller-auth", "pageserver-auth", "safekeeper-auth", "compute-auth"} {
		request.Render.Spec.Secrets[logical] = managedplatform.SecretReference{Name: logical, Revision: 1}
		request.SecretSnapshots[logical+"-r1"] = map[string][]byte{"token": []byte("old")}
	}
	compute := request.SecretSnapshots["compute-auth-r1"]
	compute["config.json"] = []byte(`{"spec":{"storage_auth_token":"old","mode":"Replica","format_version":1,"suspend_timeout_seconds":-1,"cluster":{"roles":[{"name":"cloud_admin","options":[]}],"databases":[],"settings":[]}},"compute_ctl_config":{"jwks":{"keys":[]}}}`)
	identity := databaseTLSFixture(t, []string{"neon-compute-0-control"}, false)
	compute["tls.crt"], compute["tls.key"], compute["ca.crt"] = identity["tls.crt"], identity["tls.key"], identity["ca.crt"]
	if err := PrepareNeonAuthenticationSnapshots(&request, bytes.Repeat([]byte{9}, 32), "create"); err != nil {
		t.Fatal(err)
	}
	if string(compute["token"]) != "old" || !bytes.Contains(compute["config.json"], []byte(`"old"`)) {
		t.Fatal("source secret map mutated")
	}
	if string(request.SecretSnapshots["compute-auth-r1"]["token"]) == "old" {
		t.Fatal("compute control credential was not bound to its platform")
	}
	if !bytes.Contains(request.SecretSnapshots["compute-auth-r1"]["config.json"], []byte(`"Replica"`)) {
		t.Fatal("compute mode changed")
	}
	if !bytes.Contains(request.SecretSnapshots["compute-auth-r1"]["config.json"], []byte(sqlVerifier)) {
		t.Fatal("accepted compute snapshot lacks the proxy's SQL verifier")
	}
	for _, field := range []string{"tls.crt", "ca.crt"} {
		if !bytes.Equal(request.SecretSnapshots["compute-auth-r1"][field], compute[field]) {
			t.Fatal("compute leaf TLS identity changed during authentication binding")
		}
	}
	if bytes.Equal(request.SecretSnapshots["compute-auth-r1"]["tls.key"], compute["tls.key"]) {
		t.Fatal("provider-incompatible PKCS8 key was retained in the accepted compute snapshot")
	}
	if pair, err := tls.X509KeyPair(request.SecretSnapshots["compute-auth-r1"]["tls.crt"], request.SecretSnapshots["compute-auth-r1"]["tls.key"]); err != nil || pair.PrivateKey == nil {
		t.Fatal("compute TLS key conversion changed its certificate identity", err)
	}
	for _, logical := range []string{"controller-auth", "pageserver-auth", "safekeeper-auth"} {
		data := request.SecretSnapshots[logical+"-r1"]
		if len(data["public-key.pem"]) == 0 || bytes.Equal(data["token"], []byte("old")) {
			t.Fatal("native credentials were not replaced")
		}
		for name, value := range data {
			if strings.Contains(name, "private") || bytes.Contains(value, []byte("PRIVATE KEY")) {
				t.Fatal("signing key entered snapshot")
			}
		}
	}
}

func TestNeonAuthenticationSnapshotRefusesSharedReferencesWithoutMutation(t *testing.T) {
	request := NeonRuntimeRequest{Render: managedplatform.NeonRenderInput{PlatformID: strings.Repeat("a", 32), Spec: managedplatform.Spec{Kind: "neon", Secrets: map[string]managedplatform.SecretReference{
		"controller-auth": {Name: "shared", Revision: 1}, "pageserver-auth": {Name: "shared", Revision: 1},
		"compute-auth": {Name: "shared", Revision: 1},
	}}}, SecretSnapshots: map[string]map[string][]byte{"shared-r1": {"token": []byte("unchanged")}}}
	request.SecretSnapshots["shared-r1"]["config.json"] = []byte(validNeonComputeTemplate)
	if err := PrepareNeonAuthenticationSnapshots(&request, bytes.Repeat([]byte{9}, 32), "create"); err == nil || !strings.Contains(err.Error(), "separate secret references") {
		t.Fatal("shared secret reference accepted")
	}
	if !reflect.DeepEqual(request.SecretSnapshots, map[string]map[string][]byte{"shared-r1": {"token": []byte("unchanged"), "config.json": []byte(validNeonComputeTemplate)}}) {
		t.Fatal("rejected request changed snapshots")
	}
}

func TestNeonComputeTemplateRejectsMalformedClusterBeforeAcceptance(t *testing.T) {
	for _, raw := range []string{`{"spec":{}}`, `{"spec":{"cluster":null}}`, `{"spec":{"cluster":{"settings":"invalid"}}}`, `{"spec":{"cluster":{"settings":[1]}}}`, `{"spec":{"cluster":{"settings":[{}]}}}`, `{"spec":{"cluster":{}}}`, `{"spec":{"cluster":{}},"compute_ctl_config":[]}`, strings.Repeat("x", 65537)} {
		if ValidateNeonComputeTemplate([]byte(raw)) == nil {
			t.Fatal("malformed compute template accepted")
		}
	}
	if err := ValidateNeonComputeTemplate([]byte(`{"spec":{"format_version":1,"suspend_timeout_seconds":-1,"cluster":{"roles":[],"databases":[],"settings":[{"name":"shared_buffers","value":"128MB","vartype":"string"}]}},"compute_ctl_config":{"jwks":{"keys":[]}}}`)); err != nil {
		t.Fatal(err)
	}
}

func TestNeonComputeTemplateRejectsRequiredProviderFieldsBeforeAcceptance(t *testing.T) {
	for _, test := range []struct {
		name   string
		path   []string
		value  any
		remove bool
	}{
		{"format missing", []string{"spec", "format_version"}, nil, true},
		{"format null", []string{"spec", "format_version"}, nil, false},
		{"format wrong type", []string{"spec", "format_version"}, "1", false},
		{"format overflow", []string{"spec", "format_version"}, 1e100, false},
		{"suspend missing", []string{"spec", "suspend_timeout_seconds"}, nil, true},
		{"suspend fractional", []string{"spec", "suspend_timeout_seconds"}, 0.5, false},
		{"suspend overflow", []string{"spec", "suspend_timeout_seconds"}, json.Number("9223372036854775808"), false},
		{"cluster missing", []string{"spec", "cluster"}, nil, true},
		{"roles missing", []string{"spec", "cluster", "roles"}, nil, true},
		{"roles null", []string{"spec", "cluster", "roles"}, nil, false},
		{"roles malformed", []string{"spec", "cluster", "roles"}, []any{map[string]any{"name": 1}}, false},
		{"roles missing name", []string{"spec", "cluster", "roles"}, []any{map[string]any{}}, false},
		{"roles invalid option", []string{"spec", "cluster", "roles"}, []any{map[string]any{"name": "app", "options": []any{map[string]any{"name": "LOGIN"}}}}, false},
		{"roles bound", []string{"spec", "cluster", "roles"}, make([]any, 129), false},
		{"databases missing", []string{"spec", "cluster", "databases"}, nil, true},
		{"databases null", []string{"spec", "cluster", "databases"}, nil, false},
		{"database owner missing", []string{"spec", "cluster", "databases"}, []any{map[string]any{"name": "app"}}, false},
		{"database owner wrong type", []string{"spec", "cluster", "databases"}, []any{map[string]any{"name": "app", "owner": true}}, false},
		{"settings vartype missing", []string{"spec", "cluster", "settings"}, []any{map[string]any{"name": "work_mem", "value": "4MB"}}, false},
		{"settings value wrong type", []string{"spec", "cluster", "settings"}, []any{map[string]any{"name": "work_mem", "value": 4, "vartype": "string"}}, false},
		{"settings bound", []string{"spec", "cluster", "settings"}, make([]any, 257), false},
		{"control missing", []string{"compute_ctl_config"}, nil, true},
		{"jwks missing", []string{"compute_ctl_config", "jwks"}, nil, true},
		{"jwks null", []string{"compute_ctl_config", "jwks"}, nil, false},
		{"keys missing", []string{"compute_ctl_config", "jwks", "keys"}, nil, true},
		{"keys null", []string{"compute_ctl_config", "jwks", "keys"}, nil, false},
		{"keys malformed", []string{"compute_ctl_config", "jwks", "keys"}, []any{map[string]any{}}, false},
		{"keys incomplete", []string{"compute_ctl_config", "jwks", "keys"}, []any{map[string]any{"kty": "RSA", "n": "public"}}, false},
		{"tls incomplete", []string{"compute_ctl_config", "tls"}, map[string]any{"key_path": "/tls/key"}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			var root map[string]any
			if err := json.Unmarshal([]byte(validNeonComputeTemplate), &root); err != nil {
				t.Fatal(err)
			}
			parent := root
			for _, key := range test.path[:len(test.path)-1] {
				parent = parent[key].(map[string]any)
			}
			key := test.path[len(test.path)-1]
			if test.remove {
				delete(parent, key)
			} else {
				parent[key] = test.value
			}
			raw, err := json.Marshal(root)
			if err != nil {
				t.Fatal(err)
			}
			if ValidateNeonComputeTemplate(raw) == nil {
				t.Fatal("provider-invalid template accepted")
			}
		})
	}
}

func TestNeonComputeTemplateAllowsProviderDefaultsAndPreservesIntegerBounds(t *testing.T) {
	for _, field := range []string{"format_version", "suspend_timeout_seconds", "roles", "databases", "jwks", "keys"} {
		changed := strings.Replace(validNeonComputeTemplate, `"`+field+`":`, `"`+strings.ToUpper(field)+`":`, 1)
		if ValidateNeonComputeTemplate([]byte(changed)) == nil {
			t.Fatal("provider field with incorrect case was accepted")
		}
	}
	for _, raw := range []string{
		validNeonComputeTemplate,
		`{"spec":{"format_version":1,"suspend_timeout_seconds":0,"cluster":{"roles":[{"name":"app","options":[{"name":"LOGIN","vartype":"string"}]}],"databases":[{"name":"app","owner":"app"}]}},"compute_ctl_config":{"jwks":{"keys":[]}}}`,
		`{"spec":{"format_version":1,"suspend_timeout_seconds":-1,"cluster":{"roles":[],"databases":[],"settings":null}},"compute_ctl_config":{"jwks":{"keys":[{"kty":"OKP","crv":"Ed25519","x":"public"}]},"tls":null}}`,
	} {
		if err := ValidateNeonComputeTemplate([]byte(raw)); err != nil {
			t.Fatal(err)
		}
	}
	for _, number := range []string{"-9223372036854775808", "9223372036854775807"} {
		raw := []byte(strings.Replace(validNeonComputeTemplate, `"suspend_timeout_seconds":-1`, `"suspend_timeout_seconds":`+number, 1))
		if err := ValidateNeonComputeTemplate(raw); err != nil {
			t.Fatal(err)
		}
		bound, err := bindNeonComputeConfig(raw, strings.Repeat("1", 32), strings.Repeat("2", 32), []string{"sk0:5454", "sk1:5454", "sk2:5454"}, "compute-0")
		if err != nil || !bytes.Contains(bound, []byte(`"suspend_timeout_seconds":`+number)) {
			t.Fatal("binding changed an integer accepted by the provider")
		}
		tenant, timeline := strings.Repeat("1", 32), strings.Repeat("2", 32)
		state := managedplatform.NeonControllerState{Attach: &managedplatform.NeonAttachNotification{TenantID: tenant, Shards: []managedplatform.NeonAttachShard{{NodeID: 1}}}, Safekeepers: &managedplatform.NeonSafekeeperNotification{TenantID: tenant, TimelineID: timeline, Generation: 1, Safekeepers: []managedplatform.NeonSafekeeperMember{{ID: 1}, {ID: 2}, {ID: 3}}}}
		routed, err := managedplatform.BindNeonControllerRouting(bound, strings.Repeat("a", 32), tenant, timeline, 2, state)
		if err != nil || !bytes.Contains(routed, []byte(`"suspend_timeout_seconds":`+number)) {
			t.Fatal("controller routing changed a validated template integer")
		}
	}
}

func TestNeonAuthenticationSnapshotFailureIsAtomic(t *testing.T) {
	request := NeonRuntimeRequest{Render: managedplatform.NeonRenderInput{PlatformID: strings.Repeat("a", 32), Spec: managedplatform.Spec{Kind: "neon", Secrets: map[string]managedplatform.SecretReference{}}}, SecretSnapshots: map[string]map[string][]byte{}}
	for _, logical := range []string{"controller-auth", "pageserver-auth", "safekeeper-auth", "compute-auth"} {
		request.Render.Spec.Secrets[logical] = managedplatform.SecretReference{Name: logical, Revision: 1}
		request.SecretSnapshots[logical+"-r1"] = map[string][]byte{"token": []byte("unchanged")}
	}
	if err := PrepareNeonAuthenticationSnapshots(&request, bytes.Repeat([]byte{9}, 32), "create"); err == nil {
		t.Fatal("missing compute spec accepted")
	}
	for _, snapshot := range request.SecretSnapshots {
		if len(snapshot) != 1 || string(snapshot["token"]) != "unchanged" {
			t.Fatal("failed preparation changed original snapshots")
		}
	}
}

func TestNeonDeleteAuthenticationDoesNotRequireComputeTemplate(t *testing.T) {
	request := NeonRuntimeRequest{Render: managedplatform.NeonRenderInput{PlatformID: strings.Repeat("a", 32), Spec: managedplatform.Spec{Kind: "neon", Secrets: map[string]managedplatform.SecretReference{}}}, SecretSnapshots: map[string]map[string][]byte{}}
	for _, logical := range []string{"controller-auth", "pageserver-auth", "safekeeper-auth", "compute-auth"} {
		request.Render.Spec.Secrets[logical] = managedplatform.SecretReference{Name: logical, Revision: 1}
		request.SecretSnapshots[logical+"-r1"] = map[string][]byte{"token": []byte("unchanged")}
	}
	delete(request.SecretSnapshots["compute-auth-r1"], "token")
	request.SecretSnapshots["compute-auth-r1"]["config.json"] = []byte(`{"spec":`)
	request.SecretSnapshots["compute-auth-r1"]["tls.crt"] = []byte("retained-certificate")
	if err := PrepareNeonAuthenticationSnapshots(&request, bytes.Repeat([]byte{9}, 32), "delete"); err != nil {
		t.Fatal(err)
	}
	if string(request.SecretSnapshots["controller-auth-r1"]["token"]) == "unchanged" {
		t.Fatal("delete snapshot lacks native storage authority")
	}
	compute := request.SecretSnapshots["compute-auth-r1"]
	if len(compute["token"]) == 0 || string(compute["config.json"]) != `{"spec":` || string(compute["tls.crt"]) != "retained-certificate" {
		t.Fatal("delete snapshot did not add compute authority while preserving retained configuration")
	}
}

func TestNeonSQLAuthenticationSnapshotFailureIsAtomic(t *testing.T) {
	request := NeonRuntimeRequest{Render: managedplatform.NeonRenderInput{PlatformID: strings.Repeat("a", 32), Spec: managedplatform.Spec{Kind: "neon", Secrets: map[string]managedplatform.SecretReference{}}}, SecretSnapshots: map[string]map[string][]byte{}}
	for _, logical := range []string{"controller-auth", "pageserver-auth", "safekeeper-auth", "compute-auth"} {
		request.Render.Spec.Secrets[logical] = managedplatform.SecretReference{Name: logical, Revision: 1}
		request.SecretSnapshots[logical+"-r1"] = map[string][]byte{"token": []byte("unchanged")}
	}
	request.SecretSnapshots["compute-auth-r1"]["config.json"] = []byte(validNeonComputeTemplate)
	request.ProxyEndpoint.Roles = map[string]managedplatform.NeonProxyRoleState{"cloud_admin": {SCRAMSecret: "invalid"}}
	before, err := json.Marshal(request.SecretSnapshots)
	if err != nil {
		t.Fatal(err)
	}
	if err := PrepareNeonAuthenticationSnapshots(&request, bytes.Repeat([]byte{9}, 32), "create"); err == nil {
		t.Fatal("invalid SQL credentials entered an accepted snapshot")
	}
	after, err := json.Marshal(request.SecretSnapshots)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("failed SQL authentication binding changed another secret snapshot")
	}
}
