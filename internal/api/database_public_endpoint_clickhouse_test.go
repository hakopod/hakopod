package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/database"
	"github.com/hakopod/hakopod/internal/store"
)

func TestClickHousePublicEndpointCapabilitiesAndPlanningStayScopedAndGated(t *testing.T) {
	s, ownerKey, owner, d := databasePublicEndpointAPIFixture(t, "clickhouse")
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
	reader, other := key("clickhouse-endpoint-reader", d.Environment), key("clickhouse-other-environment", "production")
	handler := (&Server{Store: s}).Handler()
	request := func(token, method, suffix, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "/api/v1/databases/"+d.ID+suffix, strings.NewReader(body))
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		r.Header.Set("Idempotency-Key", "clickhouse-endpoint-capabilities-fixture")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	for _, tc := range []struct {
		token  string
		status int
	}{{"", http.StatusUnauthorized}, {other, http.StatusNotFound}, {reader, http.StatusOK}, {ownerKey, http.StatusOK}} {
		response := request(tc.token, http.MethodGet, "/public-endpoint-capabilities", "")
		if response.Code != tc.status {
			t.Fatal("capability scope failure", response.Code, tc.status)
		}
		if tc.status != http.StatusOK {
			continue
		}
		var result database.PublicEndpointCapabilities
		if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if result.Engine != "clickhouse" || result.Available || result.UnavailableReason == "" || len(result.Routes) != 2 {
			t.Fatal("discovery lost qualification or transports", result)
		}
		if result.Routes[0].Purpose != "native" || result.Routes[0].Protocol != "clickhouse_native" || result.Routes[1].Purpose != "https" || result.Routes[1].Protocol != "https" || result.Routes[0].ReadOnly || result.Routes[1].ReadOnly {
			t.Fatal("ClickHouse transport descriptors are misleading", result)
		}
		if strings.Contains(response.Body.String(), "backend") || strings.Contains(response.Body.String(), "9440") {
			t.Fatal("discovery exposed private routing controls")
		}
	}
	for _, purpose := range []string{"native", "https"} {
		plan := fmt.Sprintf(`{"purpose":%q,"source_cidrs":["192.0.2.0/24"],"max_connections":32}`, purpose)
		if response := request(reader, http.MethodPost, "/public-endpoint-plan", plan); response.Code != http.StatusNotFound {
			t.Fatal("reader received publication authority", response.Code)
		}
		if response := request(ownerKey, http.MethodPost, "/public-endpoint-plan", plan); response.Code != http.StatusConflict {
			t.Fatal("unqualified ClickHouse planning was accepted", response.Code)
		}
	}
	var endpoints, reviews int
	if err := s.Pool.QueryRow(ctx, "SELECT (SELECT count(*) FROM managed_database_public_endpoints),(SELECT count(*) FROM managed_database_public_endpoint_reviews)").Scan(&endpoints, &reviews); err != nil {
		t.Fatal(err)
	}
	if endpoints != 0 || reviews != 0 {
		t.Fatal("rejected planning allocated a public endpoint")
	}
}
