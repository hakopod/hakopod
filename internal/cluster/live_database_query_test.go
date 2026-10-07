package cluster

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/database"
	"k8s.io/client-go/tools/clientcmd"
)

// This acceptance test uses an explicit disposable fixture or creates one owned database.
// SQL uses the managed application role. Plannable DDL stays in the disposable fixture.
func TestManagedDatabaseQueryLive(t *testing.T) {
	if os.Getenv("HAKOPOD_DATABASE_QUERY_TEST") != "1" {
		t.Skip("set HAKOPOD_DATABASE_QUERY_TEST=1 for named development cluster SQL acceptance")
	}
	path := os.Getenv("HAKOPOD_TEST_KUBECONFIG")
	config, err := clientcmd.LoadFromFile(path)
	if err != nil || config.CurrentContext != "k3d-hakopod-dev" {
		t.Fatal("SQL acceptance requires k3d-hakopod-dev")
	}
	c, err := New(path, developmentDatabaseOptions(t))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	var d database.Resource
	if fixture := os.Getenv("HAKOPOD_DATABASE_QUERY_FIXTURE"); fixture != "" {
		content, err := os.ReadFile(fixture)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(content, &d); err != nil {
			t.Fatal(err)
		}
	} else {
		d, _ = newRecoveryFixtureConfigured(t, ctx, c, "postgresql", "17", func(s *database.Spec) {
			s.TLS = &database.TLSConfig{Mode: "required"}
			s.Name = "database-query-development-fixture"
			s.CPU = "250m"
			s.Memory = "512Mi"
		})
	}
	if d.ID == "" || d.Project != "demo" || d.Environment != "development" || d.Spec.Engine != "postgresql" || !d.Spec.TLSRequired() {
		t.Fatal("SQL acceptance requires a demo/development PostgreSQL TLS fixture")
	}
	q := database.QueryRequest{SQL: "SELECT $1::numeric, NULL::text, current_setting('transaction_read_only')", Parameters: []any{"9007199254740993"}}
	result, err := c.QueryDatabase(ctx, d, q)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Rows) != 1 || len(result.Rows[0]) != 3 || result.Rows[0][0] == nil || *result.Rows[0][0] != "9007199254740993" || result.Rows[0][1] != nil || result.Rows[0][2] == nil || *result.Rows[0][2] != "on" {
		t.Fatalf("unexpected typed result: %#v", result)
	}
	result, err = c.QueryDatabase(ctx, d, database.QueryRequest{SQL: "SELECT generate_series(1,10000)", MaxRows: 2})
	if err != nil || !result.Truncated || len(result.Rows) != 2 {
		t.Fatalf("row bound: %#v %v", result, err)
	}
	for _, sql := range []string{"COMMIT", "SET TRANSACTION READ WRITE", "SELECT 1; SELECT 2", "COPY (SELECT 1) TO STDOUT"} {
		_, err = c.QueryDatabase(ctx, d, database.QueryRequest{SQL: sql})
		var queryErr *database.QueryError
		if !errors.As(err, &queryErr) || queryErr.Code != "database_query_statement_unsupported" {
			t.Fatalf("transaction escape accepted: %q %v", sql, err)
		}
	}
	// Plannable DDL and writes use only the application's database role.
	write := false
	_, err = c.QueryDatabase(ctx, d, database.QueryRequest{SQL: "CREATE TABLE hakopod_query_fixture AS SELECT 0::numeric AS value WHERE false", ReadOnly: &write})
	if err != nil {
		t.Fatal("application role could not create fixture table", err)
	}
	result, err = c.QueryDatabase(ctx, d, database.QueryRequest{SQL: "INSERT INTO hakopod_query_fixture(value) VALUES ($1::numeric) RETURNING value", Parameters: []any{"9007199254740993"}, ReadOnly: &write})
	if err != nil || len(result.Rows) != 1 || result.Rows[0][0] == nil || *result.Rows[0][0] != "9007199254740993" {
		t.Fatalf("application write lost exact numeric value: %#v %v", result, err)
	}
	result, err = c.QueryDatabase(ctx, d, database.QueryRequest{SQL: "SELECT value FROM hakopod_query_fixture"})
	if err != nil || len(result.Rows) != 1 || result.Rows[0][0] == nil || *result.Rows[0][0] != "9007199254740993" {
		t.Fatalf("write was not committed: %#v %v", result, err)
	}
	_, err = c.QueryDatabase(ctx, d, database.QueryRequest{SQL: "INSERT INTO hakopod_query_fixture(value) VALUES (2)"})
	if err == nil {
		t.Fatal("read-only transaction accepted mutation")
	}
	result, err = c.QueryDatabase(ctx, d, database.QueryRequest{SQL: "SELECT count(*) FROM hakopod_query_fixture"})
	if err != nil || len(result.Rows) != 1 || result.Rows[0][0] == nil || *result.Rows[0][0] != "1" {
		t.Fatalf("rejected mutation changed rows: %#v %v", result, err)
	}
	queryCtx, queryCancel := context.WithTimeout(ctx, time.Second)
	_, err = c.QueryDatabase(queryCtx, d, database.QueryRequest{SQL: "SELECT pg_sleep(10)"})
	queryCancel()
	if err == nil {
		t.Fatal("cancelled statement returned success")
	}
	t.Log("application role committed exact numeric writes and plannable DDL, rejected read-only mutation, and bounded cancellation")
	// A normal function call cannot switch the established transaction to writes.
	_, err = c.QueryDatabase(ctx, d, database.QueryRequest{SQL: "SELECT set_config('transaction_read_only','off',true)"})
	if err == nil {
		t.Fatal("transaction read-only mode changed")
	}
}
