package managedplatform

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

const testTenant = "11111111111111111111111111111111"
const testTimeline = "22222222222222222222222222222222"
const testOperation = "33333333333333333333333333333333"

func TestNeonRuntimeCreatesStorageBeforeAttachingComputeAndPersistsNoSecret(t *testing.T) {
	var calls []string
	storage := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.Path)
		if r.Header.Get("Authorization") != "Bearer storage-secret-token" {
			t.Error("missing storage authorization")
		}
		switch r.Method + " " + r.URL.Path {
		case "GET /control/v1/tenant/" + testTenant, "GET /control/v1/tenant/" + testTenant + "/timeline/" + testTimeline:
			w.WriteHeader(http.StatusNotFound)
		case "POST /v1/tenant":
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"shards":[]}`))
		case "POST /v1/tenant/" + testTenant + "/timeline":
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"timeline_id":"` + testTimeline + `","safekeepers":{"tenant_id":"` + testTenant + `","timeline_id":"` + testTimeline + `","generation":1,"safekeepers":[{"id":1,"hostname":"sk-1"},{"id":2,"hostname":"sk-2"},{"id":3,"hostname":"sk-3"}]}}`))
		default:
			t.Fatalf("unexpected storage request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer storage.Close()
	compute := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.Path)
		if r.Header.Get("Authorization") != "Bearer compute-secret-token" {
			t.Error("missing compute authorization")
		}
		if r.URL.Path == "/configure" {
			_, _ = w.Write([]byte(`{"status":"running"}`))
			return
		}
		_, _ = w.Write([]byte(`{"tenant":"` + testTenant + `","timeline":"` + testTimeline + `","status":"running","error":null}`))
	}))
	defer compute.Close()
	pool := x509.NewCertPool()
	pool.AddCert(storage.Certificate())
	pool.AddCert(compute.Certificate())
	stateDirectory := t.TempDir()
	if err := os.Chmod(stateDirectory, 0700); err != nil {
		t.Fatal(err)
	}
	runtime, err := NewNeonRuntime(NeonRuntimeConfig{StorageController: NeonControlTarget{Name: "storage", Origin: storage.URL, Token: "storage-secret-token"}, Computes: []NeonControlTarget{{Name: "primary", Origin: compute.URL, Token: "compute-secret-token"}}, StateDirectory: stateDirectory, RequestTimeout: time.Second, RootCAs: pool, TestOnlyFileState: true, allowUnqualifiedOwnershipProtocolForTest: true})
	if err != nil {
		t.Fatal(err)
	}
	raw := json.RawMessage(`{"spec":{"tenant_id":"` + testTenant + `","timeline_id":"` + testTimeline + `","safekeeper_connstrings":["sk-1:5454","sk-2:5454","sk-3:5454"],"storage_auth_token":"runtime-storage-secret"},"compute_ctl_config":{"jwks":{"keys":[]},"tls":null}}`)
	request := NeonLifecycleRequest{OperationID: testOperation, TenantID: testTenant, TimelineID: testTimeline, CreateTenant: true, ComputeConfig: map[string]json.RawMessage{"primary": raw}}
	state, err := runtime.Provision(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if !state.Complete || state.SafekeeperCount != 3 || len(state.AttachedComputes) != 1 {
		t.Fatal("lifecycle did not complete")
	}
	data, err := os.ReadFile(runtime.statePath(testOperation))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "secret") {
		t.Fatal("durable state contains credentials")
	}
	want := []string{"GET /control/v1/tenant/" + testTenant, "POST /v1/tenant", "GET /control/v1/tenant/" + testTenant + "/timeline/" + testTimeline, "POST /v1/tenant/" + testTenant + "/timeline", "POST /configure", "GET /status"}
	if strings.Join(calls, "|") != strings.Join(want, "|") {
		t.Fatalf("wrong lifecycle order: %v", calls)
	}
	before := len(calls)
	if _, err = runtime.Provision(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if len(calls) != before {
		t.Fatal("completed operation was not idempotent")
	}
}

func TestNeonRuntimeRejectsWrongComputeAttachmentAndKeepsQualificationFalse(t *testing.T) {
	if capability := NeonRuntimeQualification(); capability.Available || capability.ClusterQualified || capability.PublicQualified {
		t.Fatal("unqualified Neon runtime became available")
	}
	_, err := validateNeonLifecycleRequest(NeonLifecycleRequest{OperationID: testOperation, TenantID: testTenant, TimelineID: testTimeline, ComputeConfig: map[string]json.RawMessage{"primary": json.RawMessage(`{"spec":{"tenant_id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","timeline_id":"` + testTimeline + `","safekeeper_connstrings":["a","b","c"],"storage_auth_token":"token"},"compute_ctl_config":{}}`)}}, []NeonControlTarget{{Name: "primary"}})
	if err == nil {
		t.Fatal("wrong compute tenant was accepted")
	}
}

func TestNeonRuntimeRefusesFileStateByDefault(t *testing.T) {
	_, err := NewNeonRuntime(NeonRuntimeConfig{})
	if err == nil || !strings.Contains(err.Error(), "PostgreSQL ownership fencing") {
		t.Fatal("production accepted file-backed lifecycle state")
	}
}
