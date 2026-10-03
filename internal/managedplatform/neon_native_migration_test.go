//go:build hakopod_native_acceptance && linux

package managedplatform

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestNativeNeonMigrationRequiresExactOwnedMonotonicMove(t *testing.T) {
	for _, foreign := range []bool{false, true} {
		t.Run(strconv.FormatBool(foreign), func(t *testing.T) {
			platform, tenant, tenantToken, nodeToken := strings.Repeat("a", 32), strings.Repeat("b", 32), strings.Repeat("c", 32), strings.Repeat("d", 32)
			node, generation, migrations := int64(1), int64(4), 0
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				if req.Header.Get("Authorization") != "Bearer controller-secret-token" {
					t.Error("missing controller authority")
					w.WriteHeader(401)
					return
				}
				switch req.URL.Path {
				case "/control/v1/tenant/" + tenant + "/placement":
					token := tenantToken
					if foreign && migrations > 0 {
						token = strings.Repeat("e", 32)
					}
					json.NewEncoder(w).Encode(map[string]any{"schema_version": 1, "tenant_id": tenant, "node_id": node, "generation": generation, "ownership_state": "completed", "ownership_token": token})
				case "/control/v1/node/1", "/control/v1/node/2":
					id := int64(1)
					if strings.HasSuffix(req.URL.Path, "/2") {
						id = 2
					}
					host := "neon-pageserver-" + strconv.FormatInt(id-1, 10) + ".managed-platform-" + platform + ".svc"
					json.NewEncoder(w).Encode(map[string]any{"id": id, "availability_zone_id": "zone-a", "listen_http_addr": host, "listen_http_port": 9897, "listen_https_port": 9898, "listen_pg_addr": host, "listen_pg_port": 6400, "ownership_state": "completed", "ownership_token": nodeToken})
				case "/control/v1/tenant/" + tenant + "/migrate":
					var body struct {
						Node   int64 `json:"node_id"`
						Origin int64 `json:"origin_node_id"`
						Config struct {
							Prewarm bool `json:"prewarm"`
						} `json:"migration_config"`
					}
					if req.Method != http.MethodPut || req.Header.Get(neonOwnershipHeader) != tenantToken || json.NewDecoder(req.Body).Decode(&body) != nil || body.Node != 2 || body.Origin != 1 || body.Config.Prewarm {
						t.Error("migration did not preserve exact ownership and origin")
						w.WriteHeader(400)
						return
					}
					migrations++
					node = 2
					generation = 5
					w.Write([]byte(`null`))
				default:
					w.WriteHeader(404)
				}
			}))
			defer server.Close()
			roots := x509.NewCertPool()
			roots.AddCert(server.Certificate())
			config := NeonRuntimeConfig{RootCAs: roots, RequestTimeout: time.Second, StorageController: NeonControlTarget{Name: "controller", Origin: server.URL, Token: "controller-secret-token"}}
			claims := []DurableResourceClaim{{Component: "tenant", Kind: "neon_tenant", ResourceID: tenant + "@1:" + tenantToken, ImmutableGeneration: 4}}
			for i := int64(1); i <= 2; i++ {
				name := strconv.FormatInt(i-1, 10)
				host := "neon-pageserver-" + name + ".managed-platform-" + platform + ".svc"
				config.Pageservers = append(config.Pageservers, NeonPageserverRegistration{Name: name, NodeID: i, Generation: 1, Host: host, AvailabilityZone: "zone-a"})
				identity, _ := encodeNeonOwnedResourceID(neonStorageIdentity("pageserver", name, i, 1, host, "zone-a"), nodeToken)
				claims = append(claims, DurableResourceClaim{Component: "pageserver-registration-" + name, Kind: "runtime_component", ResourceID: identity, ImmutableGeneration: 1})
			}
			runtime := &DurableNeonRuntime{control: &NeonRuntime{config: config, client: server.Client()}}
			if _, _, err := runtime.MigrateTenantNative(context.Background(), platform, tenant, 1, 3, claims); err == nil || migrations != 0 {
				t.Fatal("unowned destination was mutated")
			}
			before, after, err := runtime.MigrateTenantNative(context.Background(), platform, tenant, 1, 2, claims)
			if foreign {
				if err == nil {
					t.Fatal("foreign tenant ownership produced migration evidence")
				}
			} else if err != nil || before != 4 || after != 5 || migrations != 1 {
				t.Fatal("exact migration did not verify", err)
			}
		})
	}
}
