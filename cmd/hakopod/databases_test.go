package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"github.com/hakopod/hakopod/internal/backup"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDatabaseCutoverCLIUsesReviewedAPIContract(t *testing.T) {
	id := strings.Repeat("a", 32)
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != "POST" || r.URL.Path != "/api/v1/databases/"+id+"/connect" || r.Header.Get("Idempotency-Key") != "stable-retry-key" {
			t.Error("incorrect API request")
		}
		var body map[string]any
		if json.NewDecoder(r.Body).Decode(&body) != nil || body["review_id"] != "review-fixture" || body["confirm_application"] != "orders" {
			t.Error("review and confirmation not preserved")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"release-fixture"}`))
	}))
	defer server.Close()
	c := &client{url: server.URL, http: server.Client()}
	if err := databaseCommand(context.Background(), c, "demo", "development", []string{"connect", id}, "", "stable-retry-key", "review-fixture", "", "orders", 0, databaseConnectionFlags{}); err != nil {
		t.Fatal(err)
	}
	if err := databaseCommand(context.Background(), c, "demo", "development", []string{"inspect", id}, "", "", "", "", "orders", 1, databaseConnectionFlags{JobID: "job-fixture"}); err == nil {
		t.Fatal("uninspected attestation submitted")
	}
	if calls != 1 {
		t.Fatal("invalid command reached server")
	}
}

func TestDatabaseImportCLIRequiresMatchingReview(t *testing.T) {
	raw := []byte("development archive fixture")
	sum := sha256.Sum256(raw)
	path := filepath.Join(t.TempDir(), "archive.dump")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	id := strings.Repeat("a", 32)
	uploads := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			_ = json.NewEncoder(w).Encode(backup.Import{ID: id, Spec: backup.ImportSpec{SourceName: "docker-fixture", Bytes: int64(len(raw)), SHA256: hex.EncodeToString(sum[:])}, ExpiresAt: time.Now().Add(time.Minute)})
		} else {
			uploads++
			body, _ := io.ReadAll(r.Body)
			if r.Method != "PUT" || r.Header.Get("Content-Type") != "application/octet-stream" || !bytes.Equal(body, raw) {
				t.Error("invalid raw upload")
			}
			_ = json.NewEncoder(w).Encode(backup.Artifact{ID: id})
		}
	}))
	defer server.Close()
	c := &client{url: server.URL, http: server.Client()}
	if err := databaseImportCommand(context.Background(), c, []string{"import", id}, path, "", "wrong-source", databaseImportFlags{}); err == nil {
		t.Fatal("wrong source confirmation accepted")
	}
	if uploads != 0 {
		t.Fatal("unconfirmed upload sent")
	}
	if err := databaseImportCommand(context.Background(), c, []string{"import", id}, path, "", "docker-fixture", databaseImportFlags{}); err != nil {
		t.Fatal(err)
	}
	if uploads != 1 {
		t.Fatal("upload missing")
	}
}
