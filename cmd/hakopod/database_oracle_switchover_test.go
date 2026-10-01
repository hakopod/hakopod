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

func TestOracleSwitchoverCLIContracts(t *testing.T) {
	id, operationID := strings.Repeat("a", 32), strings.Repeat("b", 32)
	for _, tc := range []struct {
		action string
		flags  databaseConnectionFlags
		body   map[string]any
	}{
		{"switchover-plan", databaseConnectionFlags{TargetMember: "standby-pod"}, map[string]any{"target_member": "standby-pod"}},
		{"switchover", databaseConnectionFlags{}, map[string]any{"review_id": "review-fixture", "expected_revision": float64(4), "confirm_name": "orders"}},
		{"switchover-retry", databaseConnectionFlags{OperationID: operationID}, map[string]any{"operation_id": operationID, "expected_revision": float64(4), "confirm_name": "orders"}},
		{"operation", databaseConnectionFlags{}, nil},
	} {
		t.Run(tc.action, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				method, path := "POST", "/api/v1/databases/"+id+"/"+tc.action
				if tc.action == "operation" {
					method, path = "GET", "/api/v1/database-operations/"+id
				}
				if r.Method != method || r.URL.Path != path {
					t.Errorf("wrong request: %s %s", r.Method, r.URL.Path)
				}
				if tc.action == "switchover" && r.Header.Get("Idempotency-Key") != "stable-oracle-key" {
					t.Error("retry key was lost")
				}
				if tc.body != nil {
					var body map[string]any
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Error(err)
						return
					}
					if !reflect.DeepEqual(body, tc.body) {
						t.Errorf("review contract changed: %#v", body)
					}
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]any{"id": operationID, "database_id": id, "kind": "switchover", "status": "queued"})
			}))
			defer server.Close()
			c := &client{url: server.URL, http: server.Client()}
			if err := databaseCommand(context.Background(), c, "fixture", "development", []string{tc.action, id}, "", "stable-oracle-key", "review-fixture", "", "orders", 4, tc.flags); err != nil {
				t.Fatal(err)
			}
			if calls != 1 {
				t.Fatalf("got %d requests", calls)
			}
		})
	}
}

func TestOracleSwitchoverCLIRejectsUnreviewedActions(t *testing.T) {
	id, operationID := strings.Repeat("a", 32), strings.Repeat("b", 32)
	for _, tc := range []struct {
		action, review, name string
		revision             int64
		flags                databaseConnectionFlags
	}{
		{"switchover-plan", "", "", 0, databaseConnectionFlags{}},
		{"switchover-plan", "", "", 0, databaseConnectionFlags{TargetMember: strings.Repeat("x", 254)}},
		{"switchover", "", "orders", 4, databaseConnectionFlags{}},
		{"switchover", "review", "", 4, databaseConnectionFlags{}},
		{"switchover", "review", "orders", 0, databaseConnectionFlags{}},
		{"switchover", "review", "orders", 4, databaseConnectionFlags{TargetMember: "another-standby"}},
		{"switchover-retry", "", "orders", 4, databaseConnectionFlags{}},
		{"switchover-retry", "", "", 4, databaseConnectionFlags{OperationID: operationID}},
		{"switchover-retry", "", "orders", 0, databaseConnectionFlags{OperationID: operationID}},
		{"switchover-retry", "", "orders", 4, databaseConnectionFlags{OperationID: operationID, TargetMember: "another-standby"}},
		{"show", "", "", 0, databaseConnectionFlags{OperationID: operationID}},
	} {
		if err := databaseCommand(context.Background(), nil, "fixture", "development", []string{tc.action, id}, "", "", tc.review, "", tc.name, tc.revision, tc.flags); err == nil {
			t.Errorf("unsafe %s reached the API", tc.action)
		}
	}
}
