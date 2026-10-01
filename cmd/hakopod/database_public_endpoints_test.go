package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestDatabasePublicEndpointCLIContracts(t *testing.T) {
	databaseID, endpointID, operationID := strings.Repeat("a", 32), strings.Repeat("b", 32), strings.Repeat("c", 32)
	tests := []struct {
		action   string
		id       string
		method   string
		path     string
		review   string
		revision int64
		flags    databaseConnectionFlags
		body     map[string]any
	}{
		{"public-endpoint-capabilities", databaseID, "GET", "/api/v1/databases/" + databaseID + "/public-endpoint-capabilities", "", 0, databaseConnectionFlags{}, nil},
		{"public-endpoint-list", databaseID, "GET", "/api/v1/databases/" + databaseID + "/public-endpoints", "", 0, databaseConnectionFlags{}, nil},
		{"public-endpoint-plan", databaseID, "POST", "/api/v1/databases/" + databaseID + "/public-endpoint-plan", "", 0, databaseConnectionFlags{PublicEndpointPurpose: "read_write", PublicEndpointCIDRs: "192.0.2.0/24,198.51.100.0/24", PublicEndpointMaxConnections: 32}, map[string]any{"purpose": "read_write", "source_cidrs": []any{"192.0.2.0/24", "198.51.100.0/24"}, "max_connections": float64(32)}},
		{"public-endpoint-publish", databaseID, "POST", "/api/v1/databases/" + databaseID + "/public-endpoints", "review-fixture", 4, databaseConnectionFlags{PublicEndpointRevision: 2}, map[string]any{"review_id": "review-fixture", "expected_database_revision": float64(4), "expected_endpoint_revision": float64(2)}},
		{"public-endpoint-revoke", databaseID, "DELETE", "/api/v1/databases/" + databaseID + "/public-endpoints/" + endpointID, "", 0, databaseConnectionFlags{PublicEndpointID: endpointID, PublicEndpointRevision: 2}, map[string]any{"expected_endpoint_revision": float64(2)}},
		{"public-endpoint-operation", operationID, "GET", "/api/v1/database-public-endpoint-operations/" + operationID, "", 0, databaseConnectionFlags{}, nil},
	}
	for _, test := range tests {
		t.Run(test.action, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != test.method || r.URL.Path != test.path {
					t.Errorf("wrong request: %s %s", r.Method, r.URL.Path)
				}
				if test.body != nil {
					var body map[string]any
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Error(err)
					}
					if !reflect.DeepEqual(body, test.body) {
						t.Errorf("wrong body: %#v", body)
					}
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"id": operationID})
			}))
			defer server.Close()
			c := &client{url: server.URL, http: server.Client()}
			if err := databaseCommand(context.Background(), c, "demo", "development", []string{test.action, test.id}, "", "stable-endpoint-key", test.review, "", "", test.revision, test.flags); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestDatabasePublicEndpointCLIRejectsIncompleteMutation(t *testing.T) {
	id := strings.Repeat("a", 32)
	for _, test := range []struct {
		action, review string
		revision       int64
		flags          databaseConnectionFlags
	}{
		{"public-endpoint-plan", "", 0, databaseConnectionFlags{PublicEndpointPurpose: "read_write", PublicEndpointMaxConnections: 32}},
		{"public-endpoint-publish", "", 4, databaseConnectionFlags{PublicEndpointRevision: 0}},
		{"public-endpoint-publish", "review", 0, databaseConnectionFlags{PublicEndpointRevision: 0}},
		{"public-endpoint-publish", "review", 4, databaseConnectionFlags{PublicEndpointRevision: -1}},
		{"public-endpoint-revoke", "", 0, databaseConnectionFlags{PublicEndpointRevision: 1}},
		{"public-endpoint-revoke", "", 0, databaseConnectionFlags{PublicEndpointID: id, PublicEndpointRevision: 0}},
	} {
		if err := databaseCommand(context.Background(), nil, "demo", "development", []string{test.action, id}, "", "", test.review, "", "", test.revision, test.flags); err == nil {
			t.Errorf("unsafe %s reached the API", test.action)
		}
	}
}
