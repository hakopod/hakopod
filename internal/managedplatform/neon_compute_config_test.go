package managedplatform

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
)

func TestNeonRuntimeBindingOwnsTransportAndWALAcrossRestore(t *testing.T) {
	platform, tenant, timeline, state, _ := neonControllerFixture()
	template := json.RawMessage(`{"spec":{"features":["activity_monitor_experimental","tls_experimental","tls_experimental"],"cluster":{"postgresql_conf":"work_mem = '8MB'\nSSL = off\nport=5432\nneon.safekeepers='foreign:5454'","settings":[{"name":"ssl","value":"off","vartype":"bool"},{"name":"SSL_KEY_FILE","value":"/foreign/key","vartype":"string"},{"name":"listen_addresses","value":"localhost","vartype":"string"},{"name":"primary_conninfo","value":"password=forbidden","vartype":"string"},{"name":"work_mem","value":"4MB","vartype":"string"},{"name":"neon.max_cluster_size","value":"10000","vartype":"integer"}]}},"compute_ctl_config":{"jwks":{"keys":[]}}}`)
	hosts := []string{"sk0:5454", "sk1:5454", "sk2:5454"}
	for _, replica := range []bool{false, true} {
		bound, err := BindNeonComputeRuntime(template, tenant, timeline, hosts, replica)
		if err != nil {
			t.Fatal(err)
		}
		bound, err = BindNeonTenantAuthentication(bound, bytes.Repeat([]byte{7}, 32), platform, tenant)
		if err != nil || validateComputeConfig(bound, tenant, timeline) != nil {
			t.Fatal("runtime binding did not produce valid primary or replica config")
		}
		routed, err := BindNeonControllerRouting(bound, platform, tenant, timeline, 2, state)
		if err != nil || validateComputeConfig(routed, tenant, timeline) != nil {
			t.Fatal("controller routing broke compute runtime binding")
		}
		var root map[string]any
		_ = json.Unmarshal(routed, &root)
		spec := root["spec"].(map[string]any)
		cluster := spec["cluster"].(map[string]any)
		if cluster["postgresql_conf"] != "work_mem = '8MB'" || strings.Count(string(routed), "tls_experimental") != 1 || !bytes.Contains(routed, []byte("activity_monitor_experimental")) {
			t.Fatal("owned config survived or caller configuration was lost")
		}
		values := map[string]string{}
		for _, value := range cluster["settings"].([]any) {
			setting := value.(map[string]any)
			values[setting["name"].(string)] = setting["value"].(string)
			if setting["vartype"] != "string" {
				t.Fatal("setting value can bypass PostgreSQL quoting")
			}
		}
		for name, expected := range map[string]string{"port": "55433", "listen_addresses": "*", "shared_preload_libraries": "neon", "ssl": "on", "ssl_cert_file": "server.crt", "ssl_key_file": "server.key", "hba_file": "/etc/hakopod-postgres/pg_hba.conf", "synchronous_commit": "on", "work_mem": "4MB", "neon.max_cluster_size": "10000"} {
			if values[name] != expected {
				t.Fatal("owned or caller setting changed unexpectedly")
			}
		}
		if bytes.Contains(routed, []byte("forbidden")) || bytes.Contains(routed, []byte("foreign")) {
			t.Fatal("caller routing or credentials survived")
		}
		if replica {
			if len(spec["safekeeper_connstrings"].([]any)) != 0 || !strings.Contains(values["primary_conninfo"], "neon-safekeeper-0.managed-platform-"+platform) || values["primary_slot_name"] != "repl_"+timeline+"_" {
				t.Fatal("replica routes through walproposer or foreign safekeepers")
			}
		} else if len(spec["safekeeper_connstrings"].([]any)) != 3 || values["synchronous_standby_names"] != "walproposer" || values["primary_conninfo"] != "" {
			t.Fatal("primary did not bind durable WAL quorum")
		}
		firstDigest, err := neonComputeRoutingDigest(routed)
		if err != nil {
			t.Fatal(err)
		}
		replayed, err := BindNeonComputeRuntime(routed, tenant, timeline, hosts, replica)
		if err != nil {
			t.Fatal(err)
		}
		replayed, err = BindNeonControllerRouting(replayed, platform, tenant, timeline, 2, state)
		if err != nil || !bytes.Equal(replayed, routed) {
			t.Fatal("replaying runtime bindings changed the accepted configuration")
		}
		state.Attach.Shards[0].NodeID = 1
		moved, err := BindNeonControllerRouting(routed, platform, tenant, timeline, 2, state)
		if err != nil {
			t.Fatal(err)
		}
		movedDigest, err := neonComputeRoutingDigest(moved)
		if err != nil || movedDigest == firstDigest {
			t.Fatal("pageserver migration disappeared from compute routing proof")
		}
		state.Attach.Shards[0].NodeID = 2
		restoredTimeline := strings.Repeat("d", 32)
		restored, err := BindNeonComputeRuntime(routed, tenant, restoredTimeline, hosts, replica)
		if err != nil || replica && (!bytes.Contains(restored, []byte("repl_"+restoredTimeline+"_")) || bytes.Contains(restored, []byte("repl_"+timeline+"_"))) {
			t.Fatal("restore retained an earlier replica slot or changed its role")
		}
	}
}

func TestNeonRuntimeBindingRejectsConfigInjection(t *testing.T) {
	for _, cluster := range []string{
		`{"postgresql_conf":"include='/tmp/config'"}`,
		`{"postgresql_conf":"work_mem = '4MB'\\\nssl=off"}`,
		`{"settings":[{"name":"work_mem\nssl","value":"off","vartype":"string"}]}`,
		`{"settings":[{"name":"work_mem","value":"4MB\ninclude='/tmp/config'","vartype":"integer"}]}`,
		`{"settings":[{"name":"include","value":"/tmp/config","vartype":"string"}]}`,
		`{"settings":[{"name":"work_mem","value":null,"vartype":"string"}]}`,
	} {
		raw := json.RawMessage(`{"spec":{"cluster":` + cluster + `}}`)
		if _, err := BindNeonComputeRuntime(raw, testTenant, testTimeline, []string{"sk0:5454", "sk1:5454", "sk2:5454"}, false); err == nil {
			t.Fatal("unsafe PostgreSQL configuration accepted")
		}
	}
}

func TestNeonReplicaRoutingDigestMatchesRustEmptySafekeeperProjection(t *testing.T) {
	canonical := `{"pageserver_connstring":"postgresql://pageserver-0:6400","safekeeper_connstrings":[],"safekeepers_generation":4,"tenant_id":"11111111111111111111111111111111","timeline_id":"22222222222222222222222222222222"}`
	raw := json.RawMessage(`{"spec":{"mode":"Replica",` + canonical[1:] + `}`)
	got, err := neonComputeRoutingDigest(raw)
	want := sha256.Sum256([]byte(canonical))
	if err != nil || got != hex.EncodeToString(want[:]) {
		t.Fatal("replica projection differs from provider canonical routing")
	}
}

func TestNeonReplicaValidationRejectsWriterAndUnverifiedWALPaths(t *testing.T) {
	raw := json.RawMessage(`{"spec":{"cluster":{},"storage_auth_token":"protected-token"},"compute_ctl_config":{}}`)
	bound, err := BindNeonComputeRuntime(raw, testTenant, testTimeline, []string{"sk0:5454", "sk1:5454", "sk2:5454"}, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(map[string]any){
		func(spec map[string]any) {
			spec["safekeeper_connstrings"] = []string{"sk0:5454", "sk1:5454", "sk2:5454"}
		},
		func(spec map[string]any) {
			settings := spec["cluster"].(map[string]any)["settings"].([]any)
			for _, value := range settings {
				setting := value.(map[string]any)
				if setting["name"] == "primary_conninfo" {
					setting["value"] = strings.ReplaceAll(setting["value"].(string), "sslmode=verify-full", "sslmode=disable")
				}
			}
		},
		func(spec map[string]any) { spec["cluster"] = map[string]any{} },
	} {
		var root map[string]any
		_ = json.Unmarshal(bound, &root)
		change(root["spec"].(map[string]any))
		invalid, _ := json.Marshal(root)
		if validateComputeConfig(invalid, testTenant, testTimeline) == nil {
			t.Fatal("unsafe replica compute configuration accepted")
		}
	}
}

func TestNeonPostgreSQLConfigurationPreservesWhitespaceAndQuotedValues(t *testing.T) {
	for _, conf := range []string{
		"work_mem = '4MB'",
		"work_mem\t=\t'4MB'",
		"application_name = 'it''s useful' # comment",
		"application_name = 'it\\'s useful'",
	} {
		raw, _ := json.Marshal(map[string]any{"spec": map[string]any{"cluster": map[string]any{"postgresql_conf": conf}}})
		bound, err := BindNeonComputeRuntime(raw, testTenant, testTimeline, []string{"sk0:5454", "sk1:5454", "sk2:5454"}, false)
		if err != nil {
			t.Fatalf("valid PostgreSQL assignment rejected: %v", err)
		}
		var root map[string]any
		_ = json.Unmarshal(bound, &root)
		if root["spec"].(map[string]any)["cluster"].(map[string]any)["postgresql_conf"] != conf {
			t.Fatal("optional PostgreSQL assignment changed")
		}
	}
}
