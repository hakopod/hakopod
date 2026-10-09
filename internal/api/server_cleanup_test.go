package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/store"
)

func TestServerCleanupResumeAndObserveReceipts(t *testing.T) {
	db := sourceDatabase(t)
	ctx := context.Background()
	raw, err := db.Bootstrap(ctx, "cleanup-operator")
	if err != nil {
		t.Fatal(err)
	}
	p, err := db.Authenticate(ctx, raw)
	if err != nil {
		t.Fatal(err)
	}
	p.Owner = true
	p.CredentialType = "browser"
	p.Email = "cleanup@example.test"
	var mu sync.Mutex
	stage := "unavailable"
	executes, reads := 0, 0
	var op string
	helper := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
			t.Error("authority reached helper")
		}
		if r.URL.Path == "/cleanup/preview" {
			write(w, 200, cleanupInventory{SchemaVersion: 1, ObservedAt: time.Now().UTC(), Items: []cleanupItem{}, Filesystem: map[string]int64{"capacity_bytes": 10000, "available_bytes": 5000}})
			return
		}
		if r.Method == "POST" {
			executes++
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			op, _ = body["operation_id"].(string)
		} else {
			reads++
			op = r.URL.Query().Get("operation_id")
		}
		if stage == "unavailable" {
			w.WriteHeader(503)
			return
		}
		receipt := map[string]any{"schema_version": 1, "operation_id": op, "status": stage}
		if stage != "not_started" {
			receipt["removed"] = []any{}
			receipt["skipped"] = []any{}
			receipt["uncertain"] = []any{}
			receipt["planned_bytes"] = 0
			receipt["available_before_bytes"] = 5000
		}
		if stage == "succeeded" {
			receipt["removed_bytes"] = 0
			receipt["available_after_bytes"] = 5000
			receipt["reclaimed_bytes"] = 0
		}
		write(w, 200, receipt)
	}))
	defer helper.Close()
	s := &Server{Store: db, Auth: AuthConfig{DeploymentMode: "self-hosted"}, maintenanceHTTP: &http.Client{Transport: maintenanceTestTransport{helper.URL, http.DefaultTransport}}}
	call := func(method, path, body, key string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "/"+path, strings.NewReader(body)).WithContext(context.WithValue(ctx, principalKey{}, p))
		r.Header.Set("Idempotency-Key", key)
		r.SetPathValue("id", op)
		w := httptest.NewRecorder()
		switch path {
		case "review":
			s.serverCleanupReview(w, r)
		case "execute":
			s.serverCleanupExecute(w, r)
		default:
			s.serverCleanupOperation(w, r)
		}
		return w
	}
	w := call("POST", "review", "{}", "")
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var review cleanupReview
	_ = json.Unmarshal(w.Body.Bytes(), &review)
	body := `{"review_id":"` + review.ID + `","confirmation":"remove reviewed files"}`
	w = call("POST", "execute", body, "cleanup-retry-key")
	if w.Code != 202 {
		t.Fatal(w.Code, w.Body.String())
	}
	first := op
	if first == "" {
		t.Fatal("operation not accepted")
	}
	mu.Lock()
	stage = "not_started"
	mu.Unlock()
	w = call("GET", "status", "", "")
	if w.Code != 202 || !strings.Contains(w.Body.String(), "not_started") {
		t.Fatal(w.Code, w.Body.String())
	}
	mu.Lock()
	if executes != 1 {
		t.Error("status dispatched cleanup")
	}
	stage = "interrupted"
	mu.Unlock()
	w = call("POST", "execute", body, "cleanup-retry-key")
	if w.Code != 202 || op != first || !strings.Contains(w.Body.String(), "interrupted") {
		t.Fatal(w.Code, w.Body.String())
	}
	// The helper can complete after the original HTTP connection disappears.
	mu.Lock()
	stage = "succeeded"
	mu.Unlock()
	w = call("GET", "status", "", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), "succeeded") {
		t.Fatal(w.Code, w.Body.String())
	}
	mu.Lock()
	before := executes
	mu.Unlock()
	w = call("POST", "execute", body, "cleanup-retry-key")
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	mu.Lock()
	if executes != before {
		t.Error("completed cleanup executed again")
	}
	mu.Unlock()
	if call("POST", "execute", strings.Replace(body, review.ID, store.NewID(), 1), "cleanup-retry-key").Code != 409 {
		t.Fatal("idempotency conflict accepted")
	}
	var count int
	for _, action := range []string{"server.cleanup.requested", "server.cleanup.completed"} {
		if err = db.Pool.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE resource=$1 AND action=$2`, op, action).Scan(&count); err != nil || count != 1 {
			t.Fatal(action, count, err)
		}
	}
	if reads < 2 {
		t.Fatal("helper was not observed")
	}
}

func TestCleanupReceiptValidationAndAuthorization(t *testing.T) {
	id := strings.Repeat("a", 32)
	for _, receipt := range []map[string]any{{}, {"schema_version": float64(1), "operation_id": id, "status": "unexpected"}, {"schema_version": float64(1), "operation_id": "other", "status": "not_started"}, {"schema_version": float64(1), "operation_id": id, "status": "succeeded"}} {
		if validCleanupReceipt(id, receipt) {
			t.Fatal("invalid receipt accepted", receipt)
		}
	}
	if !validCleanupReceipt(id, map[string]any{"schema_version": float64(1), "operation_id": id, "status": "not_started"}) {
		t.Fatal("valid pending receipt refused")
	}
	for _, mode := range []string{"self-hosted", "managed-cloud"} {
		s := &Server{Auth: AuthConfig{DeploymentMode: mode}}
		p := store.Principal{Admin: true, Owner: mode == "managed-cloud", CredentialType: "browser", Permissions: []string{"admin"}}
		for _, handler := range []http.HandlerFunc{s.serverCleanupReview, s.serverCleanupExecute, s.serverCleanupOperation} {
			r := httptest.NewRequest("POST", "/", strings.NewReader("{}")).WithContext(context.WithValue(context.Background(), principalKey{}, p))
			w := httptest.NewRecorder()
			handler(w, r)
			if w.Code != 403 {
				t.Fatal(mode, w.Code)
			}
		}
	}
}
