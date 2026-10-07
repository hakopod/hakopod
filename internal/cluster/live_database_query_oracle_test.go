package cluster

import (
	"context"
	"encoding/json"
	"github.com/hakopod/hakopod/internal/database"
	"k8s.io/client-go/tools/clientcmd"
	"os"
	"testing"
	"time"
)

func TestManagedOracleQueryLive(t *testing.T) {
	if os.Getenv("HAKOPOD_ORACLE_QUERY_TEST") != "1" {
		t.Skip("set HAKOPOD_ORACLE_QUERY_TEST=1 for owned development Oracle query acceptance")
	}
	path := os.Getenv("HAKOPOD_TEST_KUBECONFIG")
	config, err := clientcmd.LoadFromFile(path)
	if err != nil || config.CurrentContext != "k3d-hakopod-dev" {
		t.Fatal("Oracle query acceptance requires k3d-hakopod-dev")
	}
	c, err := New(path, developmentDatabaseOptions(t))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
	defer cancel()
	d, _ := newOracleFixture(t, ctx, c, "")

	read := func(sql string, args ...any) database.QueryResult {
		t.Helper()
		result, err := queryOracleSQLFixture(ctx, c, d, database.QueryRequest{SQL: sql, Parameters: args})
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	result := read("SELECT CAST(:1 AS NUMBER(30,0)), NULL FROM dual", json.Number("9007199254740993"))
	if len(result.Rows) != 1 || result.Rows[0][0] == nil || *result.Rows[0][0] != "9007199254740993" || result.Rows[0][1] != nil {
		t.Fatalf("numeric/null result differs: %#v", result)
	}
	write := false
	_, err = queryOracleSQLFixture(ctx, c, d, database.QueryRequest{SQL: "CREATE TABLE hakopod_query_fixture_v2(value NUMBER(30,0) PRIMARY KEY)", ReadOnly: &write, ExecutionMode: "nontransactional"})
	if err != nil {
		t.Fatal("explicit schema write", err)
	}
	result, err = queryOracleSQLFixture(ctx, c, d, database.QueryRequest{SQL: "INSERT INTO hakopod_query_fixture_v2(value) VALUES (:1)", Parameters: []any{json.Number("9007199254740993")}, ReadOnly: &write})
	if err != nil || result.Outcome != "committed" {
		t.Fatal("DML commit", err, result.Outcome)
	}
	result = read("SELECT value FROM hakopod_query_fixture_v2")
	if len(result.Rows) != 1 || result.Rows[0][0] == nil || *result.Rows[0][0] != "9007199254740993" {
		t.Fatal("write readback")
	}
	for _, statement := range []string{"INSERT INTO hakopod_query_fixture_v2 VALUES(2)", "DROP TABLE hakopod_query_fixture_v2", "COMMIT", "START TRANSACTION", "SET TRANSACTION READ WRITE", "CALL missing()"} {
		if _, err := queryOracleSQLFixture(ctx, c, d, database.QueryRequest{SQL: statement}); err == nil {
			t.Fatalf("read-only/control statement accepted: %s", statement)
		}
	}
	t.Log("Oracle native numeric binds, explicit schema writes and DML commits passed")
}

func queryOracleSQLFixture(ctx context.Context, c *Client, d database.Resource, q database.QueryRequest) (database.QueryResult, error) {
	if err := q.Validate(); err != nil {
		return database.QueryResult{}, err
	}
	return c.querySQLDriver(ctx, d, q, func(context.Context) error { return nil })
}
