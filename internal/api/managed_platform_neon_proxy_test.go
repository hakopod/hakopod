package api

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hakopod/hakopod/internal/managedplatform"
	"github.com/hakopod/hakopod/internal/store"
)

type neonProxyStoreFixture struct {
	record store.NeonProxyEndpointRecord
}

func (f neonProxyStoreFixture) NeonProxyEndpoint(_ context.Context, endpoint string) (store.NeonProxyEndpointRecord, error) {
	if endpoint != f.record.EndpointID {
		return store.NeonProxyEndpointRecord{}, fmt.Errorf("missing")
	}
	return f.record, nil
}

type neonProxyAuthorityFixture struct{}

func (neonProxyAuthorityFixture) ValidNeonProxyToken(value string) bool {
	return ConstantTimeNeonProxyToken(strings.Repeat("t", 32), value)
}
func (neonProxyAuthorityFixture) AuthorizeNeonProxy(_ context.Context, endpoint, role string) (NeonProxyAccess, error) {
	return NeonProxyAccess{RoleSecret: "SCRAM-SHA-256$4096:salt$stored:server", ProjectID: "project", AllowedIPs: []string{"10.0.0.0/24"}}, nil
}
func (neonProxyAuthorityFixture) WakeNeonCompute(_ context.Context, endpoint string) (NeonProxyRoute, error) {
	return NeonProxyRoute{Address: "neon-compute-0.ns.svc:55433", ServerName: "neon-compute-0.ns.svc", EndpointID: endpoint, ProjectID: "project", BranchID: "branch", ComputeID: "compute-0"}, nil
}

func TestNeonProxyControlPlaneRequiresServiceTokenAndMatchingSession(t *testing.T) {
	server := &Server{NeonProxyAuthority: neonProxyAuthorityFixture{}, neonProxyConcurrent: make(chan struct{}, 1)}
	handler := http.NewServeMux()
	server.registerNeonProxyControlPlane(handler)
	request := httptest.NewRequest(http.MethodGet, "/api/v1/internal/neon/proxy/wake_compute?session_id=12345678-1234-1234-1234-123456789abc&endpointish=endpoint", nil)
	request.Header.Set("X-Request-ID", "12345678-1234-1234-1234-123456789abc")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized || strings.Contains(response.Body.String(), "neon-compute") {
		t.Fatal("unauthorized Neon proxy request exposed route state")
	}
	request = httptest.NewRequest(http.MethodGet, "/api/v1/internal/neon/proxy/wake_compute?session_id=12345678-1234-1234-1234-123456789abc&endpointish=endpoint", nil)
	request.Header.Set("X-Request-ID", "12345678-1234-1234-1234-123456789abc")
	request.Header.Set("Authorization", "Bearer "+strings.Repeat("t", 32))
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "neon-compute-0.ns.svc:55433") {
		t.Fatal("authorized Neon proxy route was unavailable")
	}
}

func TestNeonProxyAccessReturnsOnlyRequestedRole(t *testing.T) {
	server := &Server{NeonProxyAuthority: neonProxyAuthorityFixture{}, neonProxyConcurrent: make(chan struct{}, 1)}
	handler := http.NewServeMux()
	server.registerNeonProxyControlPlane(handler)
	request := httptest.NewRequest(http.MethodGet, "/api/v1/internal/neon/proxy/get_endpoint_access_control?session_id=12345678-1234-1234-1234-123456789abc&endpointish=endpoint&role=app", nil)
	request.Header.Set("X-Request-ID", "12345678-1234-1234-1234-123456789abc")
	request.Header.Set("Authorization", "Bearer "+strings.Repeat("t", 32))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "SCRAM-SHA-256") || strings.Contains(response.Body.String(), "neon-compute") {
		t.Fatal("Neon access response contract changed")
	}
}

func TestConfiguredNeonProxyAuthorityAcceptsCloudAdminRole(t *testing.T) {
	const scram = "SCRAM-SHA-256$4096:c2FsdA==$c3RvcmVk:c2VydmVy"
	config := NeonProxyEndpointConfig{
		Enabled:    true,
		Address:    "neon-compute-0.managed-platform-bootstrap.svc:55433",
		ServerName: "neon-compute-0.managed-platform-bootstrap.svc",
		ProjectID:  "bootstrap",
		BranchID:   "bootstrap",
		ComputeID:  "compute-0",
		Roles: map[string]NeonProxyRoleConfig{
			"cloud_admin": {SCRAMSecret: scram},
		},
	}
	authority, err := NewConfiguredNeonProxyAuthority(strings.Repeat("t", 32), map[string]NeonProxyEndpointConfig{"bootstrap": config})
	if err != nil {
		t.Fatal(err)
	}
	access, err := authority.AuthorizeNeonProxy(context.Background(), "bootstrap", "cloud_admin")
	if err != nil || access.RoleSecret != scram {
		t.Fatal("configured Neon proxy authority did not preserve the cloud_admin role", err)
	}
	config.Roles = map[string]NeonProxyRoleConfig{"cloud admin": {SCRAMSecret: scram}}
	if _, err = NewConfiguredNeonProxyAuthority(strings.Repeat("t", 32), map[string]NeonProxyEndpointConfig{"bootstrap": config}); err == nil {
		t.Fatal("configured Neon proxy authority accepted a role with unsafe query characters")
	}
}

func TestDatabaseNeonProxyAuthorityDecryptsOwnedRouteAndExactRole(t *testing.T) {
	key := bytes.Repeat([]byte{9}, 32)
	const platformID = "11111111111111111111111111111111"
	const timelineID = "22222222222222222222222222222222"
	roles := map[string]managedplatform.NeonProxyRoleState{
		"app": {
			SCRAMSecret:            "SCRAM-SHA-256$4096:c2FsdA==$c3RvcmVk:c2VydmVy",
			AllowedIPs:             []string{"10.0.0.0/8"},
			AllowedVPCEndpointIDs:  []string{"vpce-123"},
			BlockPublicConnections: true,
		},
	}
	sealed, err := managedplatform.SealNeonProxyRoles(key, platformID, 3, platformID, roles)
	if err != nil {
		t.Fatal(err)
	}
	record := store.NeonProxyEndpointRecord{
		EndpointID:       platformID,
		PlatformID:       platformID,
		PlatformRevision: 3,
		OwnerOperationID: strings.Repeat("3", 32),
		Generation:       3,
		Enabled:          true,
		Address:          "neon-compute-0.managed-platform-" + platformID + ".svc:55433",
		ServerName:       "neon-compute-0.managed-platform-" + platformID + ".svc",
		ProjectID:        platformID,
		BranchID:         timelineID,
		ComputeID:        "compute-0",
		EncryptedRoles:   sealed,
	}
	authority, err := NewDatabaseNeonProxyAuthority(neonProxyStoreFixture{record: record}, strings.Repeat("t", 32), key)
	if err != nil {
		t.Fatal(err)
	}
	access, err := authority.AuthorizeNeonProxy(context.Background(), platformID, "app")
	if err != nil || access.RoleSecret != roles["app"].SCRAMSecret || access.ProjectID != platformID || !access.BlockPublicConnections {
		t.Fatal("database proxy authority did not return the exact role policy", err)
	}
	route, err := authority.WakeNeonCompute(context.Background(), platformID)
	if err != nil || route.Address != record.Address || route.ServerName != record.ServerName || route.BranchID != timelineID || route.ComputeID != "compute-0" {
		t.Fatal("database proxy authority did not return the owned route", err)
	}
}
