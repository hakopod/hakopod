package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/cluster"
	"github.com/hakopod/hakopod/internal/database"
	"github.com/hakopod/hakopod/internal/store"
)

func mysqlPublicEndpointAPIFixture(t *testing.T) (*store.Store, string, store.Principal, database.Resource) {
	return databasePublicEndpointAPIFixture(t, "mysql")
}

func databasePublicEndpointAPIFixture(t *testing.T, engine string) (*store.Store, string, store.Principal, database.Resource) {
	t.Helper()
	s, ctx := notificationTestDB(t), context.Background()
	token, err := s.Bootstrap(ctx, "mysql-public-endpoint-api-fixture")
	if err != nil {
		t.Fatal(err)
	}
	p, err := s.Authenticate(ctx, token)
	if err != nil {
		t.Fatal(err)
	}
	d := database.Resource{ID: store.NewID(), Project: "demo", Environment: "development", Spec: database.Spec{SchemaVersion: 1, Name: "mysql-public-endpoint-api-fixture", Engine: "mysql", Version: "8.4", Mode: "cluster", Shards: 1, Replicas: 2, CPU: "500m", Memory: "1Gi", StorageGiB: 1}, EncryptedCredentials: []byte("sealed-test-fixture")}
	if engine == "postgresql" {
		d.Spec.Engine, d.Spec.Version = "postgresql", "18"
	}
	if engine == "clickhouse" {
		d.Spec.Engine, d.Spec.Version, d.Spec.Memory = "clickhouse", "26.3", "2Gi"
	}
	d.Spec = d.Spec.WithSecureDefaults()
	if _, err = s.AcceptDatabase(ctx, p, d, 0, "create-mysql-public-endpoint-api", "create"); err != nil {
		t.Fatal(err)
	}
	created, err := s.ClaimDatabaseOperation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.RecordDatabaseStep(ctx, created, database.Observation{}, "succeeded", "ready", ""); err != nil {
		t.Fatal(err)
	}
	d, err = s.DatabaseInternal(ctx, d.ID)
	if err != nil {
		t.Fatal(err)
	}
	return s, token, p, d
}

func TestMySQLPublicEndpointCapabilitiesRespectReadScopeAndQualification(t *testing.T) {
	s, ownerKey, owner, d := mysqlPublicEndpointAPIFixture(t)
	ctx := context.Background()
	if _, err := s.Pool.Exec(ctx, "INSERT INTO environments(project,name) VALUES($1,'production')", d.Project); err != nil {
		t.Fatal(err)
	}
	key := func(name, environment string) string {
		t.Helper()
		_, token, err := s.CreateKey(ctx, owner, store.KeyInput{Name: name, Project: d.Project, Environment: environment, Permissions: []string{"deployments:read"}, ExpiresAt: time.Now().Add(time.Hour)})
		if err != nil {
			t.Fatal(err)
		}
		return token
	}
	reader, other := key("endpoint-reader", d.Environment), key("endpoint-other-environment", "production")
	handler := (&Server{Store: s}).Handler()
	request := func(token, method, path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "/api/v1"+path, strings.NewReader(body))
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		r.Header.Set("Idempotency-Key", "mysql-endpoint-capabilities-fixture")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	path := "/databases/" + d.ID + "/public-endpoint-capabilities"
	for _, tc := range []struct {
		token  string
		status int
	}{{"", http.StatusUnauthorized}, {other, http.StatusNotFound}, {reader, http.StatusOK}, {ownerKey, http.StatusOK}} {
		response := request(tc.token, http.MethodGet, path, "")
		if response.Code != tc.status {
			t.Fatalf("capability request returned %d, want %d: %s", response.Code, tc.status, response.Body.String())
		}
		if tc.status != http.StatusOK {
			continue
		}
		var result database.PublicEndpointCapabilities
		if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if result.Engine != "mysql" || result.Available || result.UnavailableReason == "" || len(result.Routes) != 2 {
			t.Fatal("discovery overstated qualification or lost Router routes", result)
		}
		if strings.Contains(response.Body.String(), "backend") || strings.Contains(response.Body.String(), "6446") {
			t.Fatal("discovery exposed private routing inputs")
		}
	}
	plan := `{"purpose":"read_write","source_cidrs":["192.0.2.0/24"],"max_connections":32}`
	if response := request(reader, http.MethodPost, "/databases/"+d.ID+"/public-endpoint-plan", plan); response.Code != http.StatusNotFound {
		t.Fatal("read capability granted publication authority", response.Code)
	}
	if response := request(ownerKey, http.MethodPost, "/databases/"+d.ID+"/public-endpoint-plan", plan); response.Code != http.StatusConflict {
		t.Fatal("unqualified MySQL planning was accepted", response.Code)
	}
	var endpoints, reviews int
	if err := s.Pool.QueryRow(ctx, "SELECT (SELECT count(*) FROM managed_database_public_endpoints),(SELECT count(*) FROM managed_database_public_endpoint_reviews)").Scan(&endpoints, &reviews); err != nil {
		t.Fatal(err)
	}
	if endpoints != 0 || reviews != 0 {
		t.Fatal("discovery or rejected planning allocated an endpoint")
	}
}

func TestDatabasePublicEndpointObservingResumeCannotBypassPublicationChecks(t *testing.T) {
	for _, engine := range []string{"mysql", "postgresql", "clickhouse"} {
		t.Run(engine, func(t *testing.T) {
			testPublicEndpointObservingResume(t, engine)
		})
	}
}

func testPublicEndpointObservingResume(t *testing.T, engine string) {
	t.Helper()
	s, _, owner, d := databasePublicEndpointAPIFixture(t, engine)
	ctx := context.Background()
	endpoint := database.PublicEndpoint{ID: store.NewID(), DatabaseID: d.ID, Revision: 1, Spec: database.PublicEndpointSpec{Purpose: "read_write", SourceCIDRs: []string{"192.0.2.0/24"}, MaxConnections: 32}, Allocation: database.PublicEndpointAllocation{ID: "allocation", Host: "database-15432.example.test", Address: "192.0.2.10", Port: 15432}, Status: "pending"}
	if engine == "clickhouse" {
		endpoint.Spec.Purpose = "native"
	}
	if _, err := s.Pool.Exec(ctx, "INSERT INTO managed_database_public_endpoints(id,database_id,revision,spec,allocation,status) VALUES($1,$2,$3,$4,$5,$6)", endpoint.ID, d.ID, endpoint.Revision, store.JSON(endpoint.Spec), store.JSON(endpoint.Allocation), endpoint.Status); err != nil {
		t.Fatal(err)
	}
	opID := store.NewID()
	review := database.PublicEndpointReview{Spec: endpoint.Spec, Allocation: endpoint.Allocation}
	if _, err := s.Pool.Exec(ctx, "INSERT INTO managed_database_public_endpoint_operations(id,endpoint_id,database_id,revision,identity_id,key_id,idempotency_key,request_hash,kind,review,status,phase) VALUES($1,$2,$3,$4,$5,$6,$7,$8,'publish',$9,'queued','observing')", opID, endpoint.ID, d.ID, endpoint.Revision, owner.ID, owner.KeyID, "mysql-observing-resume", []byte("fixture-request-hash"), store.JSON(review)); err != nil {
		t.Fatal(err)
	}
	// No Kubernetes runtime is needed to prove the gate is checked first.
	// Closure remains queued until a runtime can acknowledge closed sessions.
	(&Server{Store: s, Cluster: &cluster.Client{}}).reconcileDatabasePublicEndpoint(ctx)
	op, err := s.DatabasePublicEndpointOperation(ctx, owner, opID)
	if err != nil {
		t.Fatal(err)
	}
	wantMessage := "publication is unavailable"
	wantPhase := "observing"
	if engine == "postgresql" {
		wantMessage = "configuration changed"
	}
	if op.Status != "queued" || op.Phase != wantPhase || !strings.Contains(op.Message, wantMessage) {
		t.Fatal("resumed observation bypassed qualification or skipped closure", op.Status, op.Phase, op.Message)
	}
	current, err := s.DatabasePublicEndpointInternal(ctx, endpoint.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.Status != "pending" || current.Observation.Configured {
		t.Fatal("unqualified publication became active", current.Status)
	}
}

func TestMySQLPublicEndpointInventoryAndRevocationSurviveQualificationGate(t *testing.T) {
	testPublicEndpointInventoryAndRevocationSurviveQualificationGate(t, "mysql")
}

func TestClickHousePublicEndpointInventoryAndRevocationSurviveQualificationGate(t *testing.T) {
	testPublicEndpointInventoryAndRevocationSurviveQualificationGate(t, "clickhouse")
}

func testPublicEndpointInventoryAndRevocationSurviveQualificationGate(t *testing.T, engine string) {
	t.Helper()
	s, token, _, d := databasePublicEndpointAPIFixture(t, engine)
	ctx := context.Background()
	endpoint := database.PublicEndpoint{ID: store.NewID(), DatabaseID: d.ID, Revision: 1, Spec: database.PublicEndpointSpec{Purpose: "read_write", SourceCIDRs: []string{"192.0.2.0/24"}, MaxConnections: 32}, Allocation: database.PublicEndpointAllocation{ID: "allocation", Host: "database-15432.example.test", Address: "192.0.2.10", Port: 15432}, Status: "active"}
	if engine == "clickhouse" {
		endpoint.Spec.Purpose = "native"
	}
	if _, err := s.Pool.Exec(ctx, "INSERT INTO managed_database_public_endpoints(id,database_id,revision,spec,allocation,status) VALUES($1,$2,$3,$4,$5,$6)", endpoint.ID, d.ID, endpoint.Revision, store.JSON(endpoint.Spec), store.JSON(endpoint.Allocation), endpoint.Status); err != nil {
		t.Fatal(err)
	}
	handler := (&Server{Store: s}).Handler()
	for _, tc := range []struct {
		method, suffix, body string
		status               int
	}{
		{http.MethodGet, "", "", http.StatusOK},
		{http.MethodDelete, "/" + endpoint.ID, `{"expected_endpoint_revision":1}`, http.StatusAccepted},
	} {
		r := httptest.NewRequest(tc.method, "/api/v1/databases/"+d.ID+"/public-endpoints"+tc.suffix, strings.NewReader(tc.body))
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Idempotency-Key", "mysql-endpoint-revoke-fixture")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != tc.status {
			t.Fatal("qualification gate blocked inventory or revocation", tc.method, w.Code, w.Body.String())
		}
	}
}
