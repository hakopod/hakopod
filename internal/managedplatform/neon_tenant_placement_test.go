package managedplatform

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNeonTenantPlacementRequiresMonotonicSameOwner(t *testing.T) {
	tenant, token := strings.Repeat("a", 32), strings.Repeat("b", 32)
	prior := tenant + "@1:" + token
	for _, tc := range []struct {
		name, id   string
		generation int64
		allowed    bool
	}{
		{"same", prior, 4, true}, {"move", tenant + "@2:" + token, 5, true},
		{"regressed", prior, 3, false}, {"move without generation", tenant + "@2:" + token, 4, false},
		{"foreign token", tenant + "@2:" + strings.Repeat("c", 32), 5, false},
		{"foreign tenant", strings.Repeat("d", 32) + "@2:" + token, 5, false},
		{"invalid node", tenant + "@0:" + token, 5, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := ValidateNeonTenantPlacement(prior, 4, tc.id, tc.generation); (err == nil) != tc.allowed {
				t.Fatalf("placement allowed=%v: %v", tc.allowed, err)
			}
		})
	}
}

func TestNeonTenantPlacementReadsAuthenticatedExactProviderState(t *testing.T) {
	platform, tenant, token, nodeToken := strings.Repeat("a", 32), strings.Repeat("b", 32), strings.Repeat("c", 32), strings.Repeat("d", 32)
	host := "neon-pageserver-1.managed-platform-" + platform + ".svc"
	for _, tc := range []struct {
		name, state, tenantToken, nodeToken string
		node, generation                    int64
		allowed                             bool
	}{
		{"same owner move", "completed", token, nodeToken, 2, 5, true},
		{"foreign token", "completed", strings.Repeat("e", 32), nodeToken, 2, 5, false},
		{"deleting", "deleting", token, nodeToken, 2, 5, false},
		{"callback differs", "completed", token, nodeToken, 1, 5, false},
		{"regressed", "completed", token, nodeToken, 2, 3, false},
		{"foreign registration", "completed", token, strings.Repeat("f", 32), 2, 5, false},
		{"deleting registration", "completed", token, nodeToken, 2, 5, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer controller-secret-token" {
					t.Error("controller authentication missing")
					w.WriteHeader(401)
					return
				}
				switch r.URL.Path {
				case "/control/v1/tenant/" + tenant + "/placement":
					json.NewEncoder(w).Encode(map[string]any{"schema_version": 1, "tenant_id": tenant, "node_id": tc.node, "generation": tc.generation, "ownership_token": tc.tenantToken, "ownership_state": tc.state})
				case "/debug/v1/inspect":
					t.Error("callback used stale reconciliation-local generation")
					json.NewEncoder(w).Encode(map[string]any{"attachment": []int64{4, tc.node}})
				case "/control/v1/node/2":
					state := "completed"
					if tc.name == "deleting registration" {
						state = "deleting"
					}
					json.NewEncoder(w).Encode(map[string]any{"id": 2, "availability_zone_id": "zone-b", "listen_http_addr": host, "listen_http_port": 9897, "listen_https_port": 9898, "listen_pg_addr": host, "listen_pg_port": 6400, "ownership_token": tc.nodeToken, "ownership_state": state})
				default:
					w.WriteHeader(404)
				}
			}))
			defer server.Close()
			roots := x509.NewCertPool()
			roots.AddCert(server.Certificate())
			prior := DurableResourceClaim{Component: "tenant", Kind: "neon_tenant", ResourceID: tenant + "@1:" + token, ImmutableGeneration: 4}
			identity := neonStorageIdentity("pageserver", "1", 2, 1, host, "zone-b")
			owned, _ := encodeNeonOwnedResourceID(identity, nodeToken)
			registration := DurableResourceClaim{Component: "pageserver-registration-1", Kind: "runtime_component", ResourceID: owned, ImmutableGeneration: 1}
			id, generation, err := ObserveNeonTenantPlacement(context.Background(), NeonControlTarget{Name: "controller", Origin: server.URL, Token: "controller-secret-token"}, roots, platform, tenant, 2, 2, prior, registration)
			if (err == nil) != tc.allowed {
				t.Fatalf("allowed=%v: %v", tc.allowed, err)
			}
			if tc.allowed && (id != tenant+"@2:"+token || generation != 5) {
				t.Fatal("observed placement changed")
			}
		})
	}
}
