package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hakopod/hakopod/internal/database"
	"github.com/hakopod/hakopod/internal/store"
)

func TestDatabasePublicEndpointRoutesListRevokeAndReadOperation(t *testing.T) {
	s := notificationTestDB(t)
	ctx := context.Background()
	token, err := s.Bootstrap(ctx, "public-endpoint-api-fixture")
	if err != nil {
		t.Fatal(err)
	}
	p, err := s.Authenticate(ctx, token)
	if err != nil {
		t.Fatal(err)
	}
	d := database.Resource{ID: store.NewID(), Project: "demo", Environment: "development", Spec: database.Spec{SchemaVersion: 1, Name: "public-endpoint-api-fixture", Engine: "postgresql", Version: "18", Mode: "standalone", Shards: 1, CPU: "100m", Memory: "256Mi", StorageGiB: 1}, EncryptedCredentials: []byte("sealed-test-fixture")}
	d.Spec = d.Spec.WithSecureDefaults()
	if _, err = s.AcceptDatabase(ctx, p, d, 0, "create-public-endpoint-api", "create"); err != nil {
		t.Fatal(err)
	}
	created, err := s.ClaimDatabaseOperation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.RecordDatabaseStep(ctx, created, database.Observation{}, "succeeded", "ready", ""); err != nil {
		t.Fatal(err)
	}
	endpoint := database.PublicEndpoint{ID: store.NewID(), DatabaseID: d.ID, Revision: 1, Spec: database.PublicEndpointSpec{Purpose: "read_write", SourceCIDRs: []string{"192.0.2.0/24"}, MaxConnections: 32}, Allocation: database.PublicEndpointAllocation{ID: "allocation", Host: "database-15432.example.test", Address: "192.0.2.10", Port: 15432}, Status: "active"}
	if _, err = s.Pool.Exec(ctx, "INSERT INTO managed_database_public_endpoints(id,database_id,revision,spec,allocation,status) VALUES($1,$2,$3,$4,$5,$6)", endpoint.ID, endpoint.DatabaseID, endpoint.Revision, store.JSON(endpoint.Spec), store.JSON(endpoint.Allocation), endpoint.Status); err != nil {
		t.Fatal(err)
	}
	handler := (&Server{Store: s}).Handler()
	request := func(method, path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "/api/v1"+path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+token)
		if method == http.MethodDelete || method == http.MethodPost {
			r.Header.Set("Idempotency-Key", "public-endpoint-api-key")
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	listed := request(http.MethodGet, "/databases/"+d.ID+"/public-endpoints", "")
	if listed.Code != http.StatusOK || !strings.Contains(listed.Body.String(), endpoint.Allocation.Host) {
		t.Fatal("endpoint list failed", listed.Code, listed.Body.String())
	}
	planned := request(http.MethodPost, "/databases/"+d.ID+"/public-endpoint-plan", `{"purpose":"read_write","source_cidrs":["192.0.2.0/24"],"max_connections":32}`)
	if planned.Code != http.StatusServiceUnavailable {
		t.Fatal("unconfigured operator endpoint planning did not fail closed", planned.Code, planned.Body.String())
	}
	revoked := request(http.MethodDelete, "/databases/"+d.ID+"/public-endpoints/"+endpoint.ID, `{"expected_endpoint_revision":1}`)
	if revoked.Code != http.StatusAccepted {
		t.Fatal("endpoint revocation was not accepted", revoked.Code, revoked.Body.String())
	}
	var operation database.PublicEndpointOperation
	if err = json.Unmarshal(revoked.Body.Bytes(), &operation); err != nil {
		t.Fatal(err)
	}
	read := request(http.MethodGet, "/database-public-endpoint-operations/"+operation.ID, "")
	if read.Code != http.StatusOK || !strings.Contains(read.Body.String(), endpoint.ID) {
		t.Fatal("endpoint operation lookup failed", read.Code, read.Body.String())
	}
}
