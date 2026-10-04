package managedplatform

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func neonControllerFixture() (string, string, string, NeonControllerState, json.RawMessage) {
	platform, tenant, timeline := strings.Repeat("a", 32), strings.Repeat("b", 32), strings.Repeat("c", 32)
	state := NeonControllerState{Attach: &NeonAttachNotification{TenantID: tenant, Shards: []NeonAttachShard{{NodeID: 2, ShardNumber: 0}}}, Safekeepers: &NeonSafekeeperNotification{TenantID: tenant, TimelineID: timeline, Generation: 4, Safekeepers: []NeonSafekeeperMember{{ID: 3}, {ID: 1}, {ID: 2}}}}
	raw := json.RawMessage(`{"spec":{"tenant_id":"` + tenant + `","timeline_id":"` + timeline + `","pageserver_connection_info":{},"pageserver_connstring":"postgresql://foreign.invalid","cluster":{"settings":[{"name":"neon.pageserver_connstring","value":"foreign"},{"name":"neon.safekeeper_conninfo_options","value":"sslmode=disable"},{"name":"work_mem","value":"4MB"}]}},"compute_ctl_config":{}}`)
	return platform, tenant, timeline, state, raw
}

func TestNeonComputeNotificationFailedTargetDoesNotStarveLaterComputes(t *testing.T) {
	platform, tenant, timeline, state, raw := neonControllerFixture()
	var configured atomic.Uint32
	raw, err := BindNeonControllerRouting(raw, platform, tenant, timeline, 2, state)
	if err != nil {
		t.Fatal(err)
	}
	desired, err := neonComputeRoutingDigest(raw)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		index := -1
		for i := 0; i < 6; i++ {
			if r.Header.Get("Authorization") == "Bearer "+strings.Repeat(string(rune('a'+i)), 32) {
				index = i
			}
		}
		if index < 0 {
			w.WriteHeader(401)
			return
		}
		if index == 0 {
			<-r.Context().Done()
			return
		}
		if r.URL.Path == "/configure" {
			configured.Or(1 << index)
		}
		routing := strings.Repeat("d", 64)
		if configured.Load()&(1<<index) != 0 {
			routing = desired
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"tenant": tenant, "timeline": timeline, "status": "running", "operation_uuid": fmt.Sprintf("%032x", index+1), "spec_sha256": strings.Repeat("a", 64), "authorized_spec_sha256": strings.Repeat("a", 64), "routing_sha256": routing})
	}))
	defer server.Close()
	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	targets := []NeonControlTarget{}
	claims := []DurableResourceClaim{}
	configs := map[string]json.RawMessage{}
	for i := 0; i < 6; i++ {
		name := fmt.Sprintf("compute-%d", i)
		target := NeonControlTarget{Name: name, Origin: server.URL, Token: strings.Repeat(string(rune('a'+i)), 32)}
		_, identity, err := neonComputeIdentity(target)
		if err != nil {
			t.Fatal(err)
		}
		resource, err := encodeNeonComputeClaimResourceID(identity, fmt.Sprintf("%032x", i+1))
		if err != nil {
			t.Fatal(err)
		}
		targets = append(targets, target)
		claims = append(claims, DurableResourceClaim{Component: "compute-" + name, Kind: "runtime_component", ResourceID: resource, ImmutableGeneration: 1})
		configs[name] = raw
	}
	started := time.Now()
	if err := ApplyNeonComputeNotification(context.Background(), targets, roots, configs, claims, tenant, timeline); err == nil {
		t.Fatal("failed compute was acknowledged")
	}
	if configured.Load() != 62 {
		t.Fatal("early failure starved a later compute")
	}
	if time.Since(started) > 5*time.Second {
		t.Fatal("notification exceeded the upstream callback budget")
	}
}

func TestNeonControllerTokenIsBoundToPlatformAndRevision(t *testing.T) {
	key := []byte(strings.Repeat("k", 32))
	platform := strings.Repeat("a", 32)
	token, err := NeonControllerToken(key, platform, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !ValidNeonControllerToken(key, platform, 1, token) {
		t.Fatal("matching callback rejected")
	}
	if ValidNeonControllerToken(key, strings.Repeat("b", 32), 1, token) || ValidNeonControllerToken(key, platform, 2, token) || ValidNeonControllerToken(key, platform, 1, token+"x") || ValidNeonControllerToken(nil, platform, 1, token) {
		t.Fatal("callback token escaped its scope")
	}
}

func TestNeonControllerRoutingDerivesOwnedTLSAddresses(t *testing.T) {
	platform, tenant, timeline, state, raw := neonControllerFixture()
	bound, err := BindNeonControllerRouting(raw, platform, tenant, timeline, 2, state)
	if err != nil {
		t.Fatal(err)
	}
	var root map[string]any
	if json.Unmarshal(bound, &root) != nil {
		t.Fatal("invalid config")
	}
	spec := root["spec"].(map[string]any)
	u, err := url.Parse(spec["pageserver_connstring"].(string))
	if err != nil {
		t.Fatal(err)
	}
	if u.Host != "neon-pageserver-1.managed-platform-"+platform+".svc:6400" || u.Query().Get("sslmode") != "verify-full" || u.Query().Get("sslrootcert") != "/var/run/secrets/hakopod/pageserver-auth/ca.crt" || u.User != nil {
		t.Fatal("pageserver route is not an owned verified TLS endpoint")
	}
	if _, exists := spec["pageserver_connection_info"]; exists {
		t.Fatal("stale modern routing remained authoritative")
	}
	if spec["safekeepers_generation"] != float64(4) {
		t.Fatal("membership generation omitted")
	}
	encoded := string(bound)
	if strings.Contains(encoded, "foreign") || strings.Contains(encoded, "sslmode=disable") || !strings.Contains(encoded, "work_mem") {
		t.Fatal("stale routing remained or unrelated settings changed")
	}
	for i, host := range spec["safekeeper_connstrings"].([]any) {
		if host != "neon-safekeeper-"+string(rune('0'+i))+".managed-platform-"+platform+".svc:5454" {
			t.Fatal("safekeeper hosts are not sorted owned nodes")
		}
	}
}

func TestNeonControllerRejectsForeignNodesTenantAndUnsupportedShards(t *testing.T) {
	_, tenant, timeline, state, _ := neonControllerFixture()
	for _, change := range []func(*NeonControllerState){func(s *NeonControllerState) { s.Attach.TenantID = strings.Repeat("d", 32) }, func(s *NeonControllerState) { s.Attach.Shards[0].NodeID = 3 }, func(s *NeonControllerState) { s.Attach.Shards[0].ShardNumber = 1 }, func(s *NeonControllerState) { v := uint32(32768); s.Attach.StripeSize = &v }, func(s *NeonControllerState) { s.Safekeepers.TimelineID = strings.Repeat("d", 32) }, func(s *NeonControllerState) { s.Safekeepers.Safekeepers[1].ID = 3 }, func(s *NeonControllerState) { s.Safekeepers.Generation = 1 << 32 }} {
		encoded, _ := json.Marshal(state)
		var copy NeonControllerState
		_ = json.Unmarshal(encoded, &copy)
		change(&copy)
		if copy.Validate(tenant, timeline, 2) == nil {
			t.Fatal("invalid controller binding accepted")
		}
	}
}

func TestNeonComputeNotificationRequiresObservedOwnership(t *testing.T) {
	platform, tenant, timeline, state, raw := neonControllerFixture()
	token := strings.Repeat("e", 32)
	configured := 0
	foreign := false
	raw, err := BindNeonControllerRouting(raw, platform, tenant, timeline, 2, state)
	if err != nil {
		t.Fatal(err)
	}
	desired, err := neonComputeRoutingDigest(raw)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/configure" && r.Method == http.MethodPost {
			if r.Header.Get(neonOwnershipHeader) != token {
				t.Error("missing ownership token")
			}
			if r.Header.Get("hakopod-expected-spec-sha256") != strings.Repeat("a", 64) {
				t.Error("missing spec fence")
			}
			configured++
		}
		owned := token
		if foreign {
			owned = strings.Repeat("f", 32)
		}
		routing := strings.Repeat("d", 64)
		if configured > 0 {
			routing = desired
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"tenant": tenant, "timeline": timeline, "status": "running", "operation_uuid": owned, "spec_sha256": strings.Repeat("a", 64), "authorized_spec_sha256": strings.Repeat("a", 64), "routing_sha256": routing})
	}))
	defer server.Close()
	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	target := NeonControlTarget{Name: "compute-0", Origin: server.URL, Token: strings.Repeat("x", 32)}
	_, identity, err := neonComputeIdentity(target)
	if err != nil {
		t.Fatal(err)
	}
	resource, err := encodeNeonComputeClaimResourceID(identity, token)
	if err != nil {
		t.Fatal(err)
	}
	claim := DurableResourceClaim{Component: "compute-compute-0", Kind: "runtime_component", ResourceID: resource, ImmutableGeneration: 1}
	configs := map[string]json.RawMessage{"compute-0": raw}
	if err = ApplyNeonComputeNotification(context.Background(), []NeonControlTarget{target}, roots, configs, []DurableResourceClaim{claim}, tenant, timeline); err != nil {
		t.Fatal(err)
	}
	if configured != 1 {
		t.Fatal("owned compute was not reconfigured")
	}
	foreign = true
	if err = ApplyNeonComputeNotification(context.Background(), []NeonControlTarget{target}, roots, configs, []DurableResourceClaim{claim}, tenant, timeline); err == nil || configured != 1 {
		t.Fatal("foreign compute was modified")
	}
	if err = ApplyNeonComputeNotification(context.Background(), []NeonControlTarget{target}, roots, configs, nil, tenant, timeline); err != nil || configured != 1 {
		t.Fatal("unclaimed compute was modified")
	}
}
