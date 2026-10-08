package cluster

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/hakopod/hakopod/internal/database"
	"testing"
)

func TestSQLEngineStatementControls(t *testing.T) {
	for _, statement := range []string{"COMMIT", "ROLLBACK", "START TRANSACTION", "SET TRANSACTION READ WRITE", "CALL dangerous()", "BEGIN UPDATE things SET value=1; END;", "GRANT ALL ON app.* TO other"} {
		if _, err := queryStatementKind(statement); err == nil {
			t.Fatalf("session or procedural statement accepted: %s", statement)
		}
	}
	for _, statement := range []string{"SELECT 1", "WITH x AS (SELECT 1) SELECT * FROM x", "INSERT INTO things VALUES (?)", "CREATE TABLE things(value int)"} {
		if _, err := queryStatementKind(statement); err != nil {
			t.Fatal(err)
		}
	}
}
func TestClickHouseQueryScalarParameters(t *testing.T) {
	for _, tc := range []struct {
		value any
		want  string
	}{{nil, "\\N"}, {json.Number("9007199254740993"), "9007199254740993"}, {true, "1"}, {"private-value", "private-value"}} {
		got, err := clickHouseParameter(tc.value)
		if err != nil || got != tc.want {
			t.Fatal(got, err)
		}
	}
	if _, err := clickHouseParameter(map[string]any{}); err == nil {
		t.Fatal("complex bind accepted")
	}
}
func TestSQLQueryExecutionModes(t *testing.T) {
	request := database.QueryRequest{SQL: "SELECT 1", ExecutionMode: "fake"}
	if request.Validate() == nil {
		t.Fatal("unsupported execution mode accepted")
	}
	request.ExecutionMode = "nontransactional"
	if err := request.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestQueryProductionCapabilityGate(t *testing.T) {
	for _, engine := range []string{"mysql", "vitess", "duckdb", "clickhouse", "oracle"} {
		if database.CapabilitiesForQuery(engine).Supported {
			continue
		}
		result, err := (&Client{}).QueryDatabase(context.Background(), database.Resource{Spec: database.Spec{Engine: engine}}, database.QueryRequest{SQL: "SELECT 1"})
		var queryErr *database.QueryError
		if !errors.As(err, &queryErr) || queryErr.Code != "database_query_engine_unsupported" || result.Outcome != "" {
			t.Fatalf("unqualified engine %s reached transport: %#v %v", engine, result, err)
		}
	}
	_, err := (&Client{}).QueryDatabase(context.Background(), database.Resource{Spec: database.Spec{Engine: "postgresql"}}, database.QueryRequest{SQL: "SELECT 1", ExecutionMode: "nontransactional"})
	var queryErr *database.QueryError
	if !errors.As(err, &queryErr) || queryErr.Code != "database_query_execution_mode_unsupported" {
		t.Fatal("unsupported PostgreSQL mode", err)
	}
}

func TestVitessScopeDoesNotEnableUnqualifiedTransport(t *testing.T) {
	c := database.CapabilitiesForQuery("vitess")
	if c.TransactionScope != "single_shard" || c.CrossShardDML == nil || *c.CrossShardDML {
		t.Fatal("Vitess shard scope missing")
	}
	write := false
	for _, mode := range []string{"transaction", "nontransactional"} {
		_, err := (&Client{}).QueryDatabase(context.Background(), database.Resource{Spec: database.Spec{Engine: "vitess"}}, database.QueryRequest{SQL: "UPDATE records SET label=?", Parameters: []any{"control"}, ReadOnly: &write, ExecutionMode: mode})
		var failure *database.QueryError
		if !errors.As(err, &failure) || failure.Code != "database_query_engine_unsupported" || failure.Outcome != "not_started" {
			t.Fatal("unqualified Vitess reached transport")
		}
	}
}
