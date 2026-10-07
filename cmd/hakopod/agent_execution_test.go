package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestExecutionCommandsShareScopedAPI(t *testing.T) {
	sqlFile := t.TempDir() + "/query.sql"
	if err := os.WriteFile(sqlFile, []byte("select $1"), 0600); err != nil {
		t.Fatal(err)
	}
	calls := []string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.Path)
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Fatal("identity not preserved")
		}
		switch r.URL.Path {
		case "/api/v1/databases/db":
			io.WriteString(w, `{"project":"p","environment":"dev"}`)
		case "/api/v1/databases/db/query":
			raw, _ := io.ReadAll(r.Body)
			if !strings.Contains(string(raw), `"read_only":true`) || !strings.Contains(string(raw), `"parameters":[9007199254740993]`) {
				t.Fatal(string(raw))
			}
			io.WriteString(w, `{"outcome":"read","rows":[]}`)
		default:
			t.Fatal(r.URL.Path)
		}
	}))
	defer server.Close()
	c := &client{url: server.URL, key: "test-key", http: server.Client()}
	if err := runExecution(context.Background(), c, config{Project: "p", Environment: "dev"}, "query", "db", executionFlags{SQLFile: sqlFile, ParametersJSON: `[9007199254740993]`, MaxRows: 100, MaxBytes: 65536}); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 2 {
		t.Fatal(calls)
	}
	if err := runExecution(context.Background(), c, config{}, "exec", "app", executionFlags{CommandJSON: `["true"]`}); err == nil {
		t.Fatal("execution opt-in bypass")
	}
}
func TestResponseErrorRetainsQueryOutcome(t *testing.T) {
	res := &http.Response{StatusCode: 500, Body: io.NopCloser(strings.NewReader(`{"error":{"code":"query_failed","message":"Query failed"},"outcome":"unknown","operation_id":"operation-1"}`))}
	err := responseError(res)
	if !strings.Contains(err.Error(), "outcome=unknown") || !strings.Contains(err.Error(), "operation_id=operation-1") {
		t.Fatal(err)
	}
}
func TestSQLCLIWriteRevisionForwarding(t *testing.T) {
	file := t.TempDir() + "/query.sql"
	if err := os.WriteFile(file, []byte("update things set value=1"), 0600); err != nil {
		t.Fatal(err)
	}
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method == "GET" {
			io.WriteString(w, `{"project":"p","environment":"dev"}`)
			return
		}
		raw, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(raw), `"expected_revision":7`) || !strings.Contains(string(raw), `"read_only":false`) {
			t.Fatal(string(raw))
		}
		io.WriteString(w, `{"outcome":"committed"}`)
	}))
	defer server.Close()
	c := &client{url: server.URL, key: "test-key", http: server.Client()}
	flags := executionFlags{SQLFile: file, AllowSQLWrite: true, MaxRows: 100, MaxBytes: 65536}
	if err := runExecution(context.Background(), c, config{Project: "p", Environment: "dev"}, "query", "db", flags); err == nil {
		t.Fatal("unreviewed CLI write accepted")
	}
	if calls != 0 {
		t.Fatal(calls)
	}
	flags.ExpectedRevision = 7
	if err := runExecution(context.Background(), c, config{Project: "p", Environment: "dev"}, "query", "db", flags); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatal(calls)
	}
}
func TestClientResponsePreservesExactNumbers(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, `{"revision":9007199254740993}`) }))
	defer server.Close()
	c := &client{url: server.URL, http: server.Client()}
	var out map[string]any
	if err := c.request(context.Background(), "GET", "/applications/app", nil, "", &out); err != nil {
		t.Fatal(err)
	}
	if out["revision"] != json.Number("9007199254740993") {
		t.Fatal(out)
	}
}
