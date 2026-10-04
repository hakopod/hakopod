package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestDatabaseReplicaRetryCLIEntryPoint(t *testing.T) {
	id, operation := strings.Repeat("a", 32), strings.Repeat("b", 32)
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != "POST" || !strings.HasPrefix(r.URL.Path, "/api/v1/databases/"+id+"/resize-retry") {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"fixture"}`))
	}))
	defer srv.Close()
	t.Setenv("HAKOPOD_CONFIG", filepath.Join(t.TempDir(), "config.json"))
	t.Setenv("HAKOPOD_API_URL", srv.URL)
	t.Setenv("HAKOPOD_API_KEY", "fixture-session")
	t.Setenv("HAKOPOD_WORKSPACE", "")
	saved := os.Args
	defer func() { os.Args = saved }()
	for _, action := range []string{"resize-retry-plan", "resize-retry"} {
		os.Args = []string{"hakopod", "database", action, id, "--operation-id", operation, "--revision", "2"}
		if action == "resize-retry" {
			os.Args = append(os.Args, "--review-id", "review-fixture", "--name", "orders", "--idempotency-key", "stable-replica-retry")
		}
		if err := run(); err != nil {
			t.Fatalf("default file blocked %s: %v", action, err)
		}
		before := calls
		os.Args = append(os.Args, "--file", "hakopod.toml")
		if err := run(); err == nil || calls != before {
			t.Fatalf("explicit changed file was accepted by %s", action)
		}
	}
	if calls != 2 {
		t.Fatalf("expected two reviewed requests, got %d", calls)
	}
}

func TestDatabaseReplicaRetryCLIContract(t *testing.T) {
	id, operation := strings.Repeat("a", 32), strings.Repeat("b", 32)
	for _, action := range []string{"resize-retry-plan", "resize-retry"} {
		t.Run(action, func(t *testing.T) {
			calls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Method != "POST" || r.URL.Path != "/api/v1/databases/"+id+"/"+action {
					t.Errorf("wrong retry request: %s %s", r.Method, r.URL.Path)
				}
				want := map[string]any{"operation_id": operation, "expected_revision": float64(2)}
				if action == "resize-retry" {
					want["review_id"], want["confirm_name"] = "review-fixture", "orders"
					if r.Header.Get("Idempotency-Key") != "stable-replica-retry" {
						t.Error("retry lost its idempotency key")
					}
				}
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil || !reflect.DeepEqual(body, want) {
					t.Errorf("retry request changed its reviewed intent: %#v (%v)", body, err)
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"id":"fixture"}`))
			}))
			defer srv.Close()
			c := &client{url: srv.URL, http: srv.Client()}
			if err := databaseCommand(context.Background(), c, "fixture", "development", []string{action, id}, "", "stable-replica-retry", "review-fixture", "", "orders", 2, databaseConnectionFlags{OperationID: operation}); err != nil {
				t.Fatal(err)
			}
			if calls != 1 {
				t.Fatalf("expected one reviewed request, got %d", calls)
			}
		})
	}
}

func TestDatabaseReplicaRetryCLIRejectsChangedOrUnreviewedIntent(t *testing.T) {
	id, operation := strings.Repeat("a", 32), strings.Repeat("b", 32)
	for _, tc := range []struct {
		action, file, review, name, key, operation string
		revision                                   int64
	}{
		{"resize-retry-plan", "changed.toml", "", "", "", operation, 2},
		{"resize-retry-plan", "", "", "", "", "", 2},
		{"resize-retry-plan", "", "", "", "", operation, 0},
		{"resize-retry", "", "", "orders", "stable-retry", operation, 2},
		{"resize-retry", "", "review", "", "stable-retry", operation, 2},
		{"resize-retry", "", "review", "orders", "", operation, 2},
		{"resize-retry", "changed.toml", "review", "orders", "stable-retry", operation, 2},
	} {
		if err := databaseCommand(context.Background(), nil, "fixture", "development", []string{tc.action, id}, tc.file, tc.key, tc.review, "", tc.name, tc.revision, databaseConnectionFlags{OperationID: tc.operation}); err == nil {
			t.Errorf("unreviewed %s reached the transport", tc.action)
		}
	}
}
