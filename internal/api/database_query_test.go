package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hakopod/hakopod/internal/database"
	"github.com/hakopod/hakopod/internal/store"
)

func TestDatabaseQueryRequestBounds(t *testing.T) {
	tests := []struct {
		name  string
		q     database.QueryRequest
		valid bool
	}{
		{"default", database.QueryRequest{SQL: "SELECT $1", Parameters: []any{"text"}}, true},
		{"empty", database.QueryRequest{}, false},
		{"large SQL", database.QueryRequest{SQL: strings.Repeat("x", database.QueryMaxSQLBytes+1)}, false},
		{"null SQL", database.QueryRequest{SQL: "SELECT\x00"}, false},
		{"negative rows", database.QueryRequest{SQL: "SELECT 1", MaxRows: -1}, false},
		{"large rows", database.QueryRequest{SQL: "SELECT 1", MaxRows: 1001}, false},
		{"small byte budget", database.QueryRequest{SQL: "SELECT 1", MaxBytes: 1023}, false},
		{"large byte budget", database.QueryRequest{SQL: "SELECT 1", MaxBytes: database.QueryMaxBytes + 1}, false},
		{"object parameter", database.QueryRequest{SQL: "SELECT $1", Parameters: []any{map[string]any{"x": "secret"}}}, false},
		{"array parameter", database.QueryRequest{SQL: "SELECT $1", Parameters: []any{[]any{1}}}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.q.Validate()
			if (err == nil) != tt.valid {
				t.Fatalf("valid=%v error=%v", tt.valid, err)
			}
			if tt.valid && (!tt.q.IsReadOnly() || tt.q.MaxRows != 100 || tt.q.MaxBytes != 256<<10) {
				t.Fatal("unsafe defaults")
			}
		})
	}
}
func TestDatabaseQueryAuthorization(t *testing.T) {
	d := database.Resource{Project: "project", Environment: "development"}
	principal := store.Principal{CredentialType: "machine", Admin: true, Permissions: []string{"databases:query"}, Project: "project", Environment: "development"}
	if !databaseQueryAllowed(principal, d, true) {
		t.Fatal("explicit scoped read grant rejected")
	}
	if databaseQueryAllowed(principal, d, false) {
		t.Fatal("read grant allowed write")
	}
	principal.Permissions = []string{"deployments:write", "deployments:read"}
	if databaseQueryAllowed(principal, d, true) {
		t.Fatal("deployment access allowed SQL")
	}
	principal.Permissions = []string{"databases:write-query"}
	if databaseQueryAllowed(principal, d, false) {
		t.Fatal("SQL write grant replaced the required query grant")
	}
	principal.Permissions = []string{"databases:query", "databases:write-query"}
	if !databaseQueryAllowed(principal, d, false) {
		t.Fatal("write grant rejected")
	}
	principal.Permissions = []string{"admin"}
	if databaseQueryAllowed(principal, d, false) {
		t.Fatal("wildcard machine grant allowed SQL")
	}
	principal.Permissions = []string{"databases:query", "databases:write-query"}
	principal.Environment = "production"
	if databaseQueryAllowed(principal, d, false) {
		t.Fatal("environment scope escaped")
	}
	principal.Environment = "development"
	principal.Application = "application"
	if databaseQueryAllowed(principal, d, false) {
		t.Fatal("application received database query access")
	}
	for _, readOnly := range []bool{true, false} {
		cli := store.Principal{Admin: true, CredentialType: "cli", Permissions: []string{"admin"}}
		if databaseQueryAllowed(cli, d, readOnly) {
			t.Fatal("administrator CLI bypassed explicit SQL consent", readOnly)
		}
		cli.Permissions = append(cli.Permissions, "databases:query")
		if !readOnly {
			cli.Permissions = append(cli.Permissions, "databases:write-query")
		}
		if !databaseQueryAllowed(cli, d, readOnly) {
			t.Fatal("explicit CLI SQL consent was rejected", readOnly)
		}
	}
}
func TestDatabaseQueryWriteRequiresCurrentQueryGrant(t *testing.T) {
	d := database.Resource{Project: "project", Environment: "development"}
	for _, credential := range []string{"machine", "cli", "browser"} {
		p := store.Principal{CredentialType: credential, Admin: true, Project: d.Project, Environment: d.Environment, Permissions: []string{"databases:query", "databases:write-query"}}
		if !databaseQueryAllowed(p, d, false) {
			t.Fatal("complete SQL grants rejected", credential)
		}
		p.Permissions = []string{"databases:write-query"}
		if databaseQueryAllowed(p, d, false) {
			t.Fatal("SQL write continued after query grant removal", credential)
		}
		if credential != "browser" {
			p.Permissions = append(p.Permissions, "admin")
			if databaseQueryAllowed(p, d, false) {
				t.Fatal("administrator wildcard replaced the removed query grant", credential)
			}
		}
	}
}
func TestDatabaseQueryErrorDoesNotExposeSQL(t *testing.T) {
	w := httptest.NewRecorder()
	writeDatabaseQueryError(w, "operation", &database.QueryError{Code: "database_query_failed", Outcome: "unknown"})
	if w.Code != 503 {
		t.Fatalf("status %d", w.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["outcome"] != "unknown" || body["operation_id"] != "operation" {
		t.Fatal(body)
	}
	if strings.Contains(w.Body.String(), "retry with backoff") {
		t.Fatal("uncertain write encourages automatic retries")
	}
}

func TestDatabaseQueryDecoderPreservesNumericParameter(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"sql":"SELECT $1::bigint","parameters":[9007199254740993]}`))
	var q database.QueryRequest
	w := httptest.NewRecorder()
	if !decodeDatabaseQuery(w, r, &q) {
		t.Fatal(w.Body.String())
	}
	number, ok := q.Parameters[0].(json.Number)
	if !ok || number.String() != "9007199254740993" {
		t.Fatalf("parameter precision lost: %#v", q.Parameters)
	}
}
