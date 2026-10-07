package cluster

import (
	"context"
	"database/sql"
	"encoding/json"
	"github.com/hakopod/hakopod/internal/database"
	"k8s.io/client-go/tools/clientcmd"
	"os"
	"testing"
	"time"
)

func TestManagedMySQLQueryLive(t *testing.T) {
	if os.Getenv("HAKOPOD_MYSQL_QUERY_TEST") != "1" {
		t.Skip("set HAKOPOD_MYSQL_QUERY_TEST=1 for owned development MySQL query acceptance")
	}
	path := os.Getenv("HAKOPOD_TEST_KUBECONFIG")
	config, err := clientcmd.LoadFromFile(path)
	if err != nil || config.CurrentContext != "k3d-hakopod-dev" {
		t.Fatal("MySQL query acceptance requires k3d-hakopod-dev")
	}
	c, err := New(path, developmentDatabaseOptions(t))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
	defer cancel()
	d, password := newMySQLFixture(t, ctx, c, "standalone")
	d.Spec.Name = "mysql-query-development-fixture"
	d.Spec.CPU = "500m"
	waitMySQLFixture(t, ctx, c, d, password)

	read := func(sql string, args ...any) database.QueryResult {
		t.Helper()
		result, err := queryMySQLFixture(ctx, c, d, database.QueryRequest{SQL: sql, Parameters: args})
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	result := read("SELECT CAST(? AS DECIMAL(30,0)), NULL", json.Number("9007199254740993"))
	if len(result.Rows) != 1 || result.Rows[0][0] == nil || *result.Rows[0][0] != "9007199254740993" || result.Rows[0][1] != nil {
		t.Fatalf("numeric/null result differs: %#v", result)
	}
	write := false
	_, err = queryMySQLFixture(ctx, c, d, database.QueryRequest{SQL: "CREATE TABLE IF NOT EXISTS hakopod_query_fixture_v2(value DECIMAL(30,0) PRIMARY KEY)", ReadOnly: &write, ExecutionMode: "nontransactional"})
	if err != nil {
		t.Fatal("explicit schema write", err)
	}
	result, err = queryMySQLFixture(ctx, c, d, database.QueryRequest{SQL: "INSERT INTO hakopod_query_fixture_v2(value) VALUES (?)", Parameters: []any{json.Number("9007199254740993")}, ReadOnly: &write})
	if err != nil || result.Outcome != "committed" {
		t.Fatal("DML commit", err, result.Outcome)
	}
	result = read("SELECT value FROM hakopod_query_fixture_v2")
	if len(result.Rows) != 1 || result.Rows[0][0] == nil || *result.Rows[0][0] != "9007199254740993" {
		t.Fatal("write readback")
	}
	for _, statement := range []string{"INSERT INTO hakopod_query_fixture_v2 VALUES(2)", "DROP TABLE hakopod_query_fixture_v2", "COMMIT", "START TRANSACTION", "SET TRANSACTION READ WRITE", "CALL missing()"} {
		if _, err := queryMySQLFixture(ctx, c, d, database.QueryRequest{SQL: statement}); err == nil {
			t.Fatalf("read-only/control statement accepted: %s", statement)
		}
	}
	short, done := context.WithTimeout(ctx, time.Second)
	_, err = queryMySQLFixture(short, c, d, database.QueryRequest{SQL: "SELECT SLEEP(10)"})
	done()
	if err == nil {
		t.Fatal("cancellation returned success")
	}

	observation, e := c.ObserveDatabase(ctx, d)
	if e != nil {
		t.Fatal(e)
	}
	var primary database.Member
	for _, member := range observation.Members {
		if member.Name == observation.Primary {
			primary = member
		}
	}
	native, e := c.mysqlQueryClient(ctx, d, primary)
	if e != nil {
		t.Fatal(e)
	}
	defer native.Close()
	// Check the server boundary directly, without the API statement classifier.
	connection, e := native.Conn(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer connection.Close()
	if _, e = connection.ExecContext(ctx, "CREATE FUNCTION app.hakopod_query_mutate() RETURNS INT MODIFIES SQL DATA BEGIN INSERT INTO hakopod_query_fixture_v2 VALUES (2); RETURN 2; END"); e != nil {
		t.Fatal("fixture function", e)
	}
	if _, e = connection.ExecContext(ctx, "SET SESSION TRANSACTION READ ONLY"); e != nil {
		t.Fatal(e)
	}
	tx, e := connection.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if e != nil {
		t.Fatal("server read-only transaction", e)
	}
	if _, e = tx.ExecContext(ctx, "SELECT app.hakopod_query_mutate()"); e == nil {
		tx.Rollback()
		t.Fatal("function mutated inside server read-only transaction")
	}
	tx.Rollback()
	tx, e = connection.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = tx.ExecContext(ctx, "CREATE TABLE app.hakopod_readonly_escape(value INT)"); e == nil {
		tx.Rollback()
		t.Fatal("implicit-commit DDL escaped server read-only transaction")
	}
	tx.Rollback()
	t.Log("MySQL app identity preserved numeric binds, explicit DDL, DML commits, read-only denial and bounded cancellation")
}

func queryMySQLFixture(ctx context.Context, c *Client, d database.Resource, q database.QueryRequest) (database.QueryResult, error) {
	if err := q.Validate(); err != nil {
		return database.QueryResult{}, err
	}
	return c.querySQLDriver(ctx, d, q, func(context.Context) error { return nil })
}
