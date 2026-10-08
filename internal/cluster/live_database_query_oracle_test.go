package cluster

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/hakopod/hakopod/internal/database"
	oranetwork "github.com/sijms/go-ora/v3/network"
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
	ctx, cancel := context.WithTimeout(context.Background(), 18*time.Minute)
	defer cancel()
	d, observed := newOracleFixture(t, ctx, c, "")
	oracleBoundQueryDiagnostic(t, ctx, c, d, observed.Members[0])

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
	limited := database.QueryRequest{SQL: "SELECT LEVEL FROM dual CONNECT BY LEVEL<=5", MaxRows: 2}
	limited.Validate()
	result, err = c.querySQLDriver(ctx, d, limited, func(context.Context) error { return nil })
	if err != nil || len(result.Rows) != 2 || !result.Truncated {
		t.Fatal("row bound", err)
	}
	smallLOB := read("SELECT XMLAGG(XMLELEMENT(e, RPAD('x',100,'x'))).GETCLOBVAL() FROM dual CONNECT BY LEVEL <= 2")
	if len(smallLOB.Rows) != 1 || len(smallLOB.Rows[0]) != 1 || smallLOB.Rows[0][0] == nil || len(*smallLOB.Rows[0][0]) < 200 {
		t.Fatal("Oracle CLOB positive control")
	}
	oversized := database.QueryRequest{SQL: "SELECT XMLAGG(XMLELEMENT(e, RPAD('x',4000,'x'))).GETCLOBVAL() FROM dual CONNECT BY LEVEL <= 600"}
	if err = oversized.Validate(); err != nil {
		t.Fatal(err)
	}
	_, err = c.querySQLDriver(ctx, d, oversized, func(context.Context) error { return nil })
	var oversizedError *database.QueryError
	if !errors.As(err, &oversizedError) || oversizedError.Code != "database_query_result_limit" || (oversizedError.Outcome != "rolled_back" && oversizedError.Outcome != "unknown") {
		t.Fatal("oversized Oracle value lacked conservative bounded outcome")
	}
	short, stop := context.WithTimeout(ctx, time.Second)
	busy := database.QueryRequest{SQL: "SELECT SUM(LEVEL) FROM dual CONNECT BY LEVEL<=1000000000"}
	busy.Validate()
	_, err = c.querySQLDriver(short, d, busy, func(context.Context) error { return nil })
	stop()
	if err == nil {
		t.Fatal("cancelled query accepted")
	}
	native, err := c.oracleApplicationConnection(ctx, d, observed.Members[0], true)
	if err != nil {
		t.Fatal("native connection")
	}
	defer native.Close()
	connection, err := native.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	tx, err := connection.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.ExecContext(ctx, "SET TRANSACTION READ ONLY"); err != nil {
		t.Fatal("server readonly setup")
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO hakopod_query_fixture_v2 VALUES (2)"); err == nil {
		tx.Rollback()
		t.Fatal("server readonly accepted DML")
	}
	tx.Rollback()
	if _, err = connection.ExecContext(ctx, "CREATE OR REPLACE FUNCTION hakopod_mutate RETURN NUMBER IS PRAGMA AUTONOMOUS_TRANSACTION; BEGIN INSERT INTO hakopod_query_fixture_v2 VALUES ((SELECT NVL(MAX(value),0)+1 FROM hakopod_query_fixture_v2)); COMMIT; RETURN 3; END;"); err != nil {
		t.Fatal("native fixture function")
	}
	var initialCount int
	if err = connection.QueryRowContext(ctx, "SELECT COUNT(*) FROM hakopod_query_fixture_v2").Scan(&initialCount); err != nil {
		t.Fatal(err)
	}
	var functionValue int
	if err = connection.QueryRowContext(ctx, "SELECT hakopod_mutate() FROM dual").Scan(&functionValue); err != nil || functionValue != 3 {
		t.Fatal("autonomous function positive control", err)
	}
	var before int
	if err = connection.QueryRowContext(ctx, "SELECT COUNT(*) FROM hakopod_query_fixture_v2").Scan(&before); err != nil || before != initialCount+1 {
		t.Fatal(err)
	}
	tx, err = connection.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, "SET TRANSACTION READ ONLY"); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRowContext(ctx, "SELECT hakopod_mutate() FROM dual").Scan(&functionValue); err == nil {
		t.Fatal("autonomous function escaped readonly transaction")
	}
	tx.Rollback()
	var after int
	if err = connection.QueryRowContext(ctx, "SELECT COUNT(*) FROM hakopod_query_fixture_v2").Scan(&after); err != nil || after != before {
		t.Fatal("readonly function changed persisted row count")
	}
	t.Log("Oracle native numeric binds, explicit schema writes and DML commits passed")
}

func queryOracleSQLFixture(ctx context.Context, c *Client, d database.Resource, q database.QueryRequest) (database.QueryResult, error) {
	if err := q.Validate(); err != nil {
		return database.QueryResult{}, err
	}
	return c.querySQLDriver(ctx, d, q, func(context.Context) error { return nil })
}

func oracleBoundQueryDiagnostic(t *testing.T, ctx context.Context, c *Client, d database.Resource, member database.Member) {
	t.Helper()
	started := time.Now()
	report := func(phase string, err error) {
		code := 0
		var native *oranetwork.OracleError
		if errors.As(err, &native) {
			code = native.ErrCode
		}
		t.Logf("Oracle synthetic phase=%s error_type=%T oracle_code=%d elapsed=%s context_done=%t", phase, err, code, time.Since(started), ctx.Err() != nil)
	}
	observationStart := time.Now()
	_, observationErr := c.ObserveDatabase(ctx, d)
	t.Logf("Oracle synthetic observation_elapsed=%s error_type=%T context_done=%t", time.Since(observationStart), observationErr, ctx.Err() != nil)
	client, err := c.oracleApplicationConnectionOptions(ctx, d, member, true, true)
	if err != nil {
		report("connect", err)
		return
	}
	defer client.Close()
	conn, err := client.Conn(ctx)
	if err != nil {
		report("connection", err)
		return
	}
	defer conn.Close()
	if err = boundOracleQueryConnection(conn); err != nil {
		report("decoder_bound", err)
		return
	}
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		report("begin", err)
		return
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, "SET TRANSACTION READ ONLY"); err != nil {
		report("readonly_setup", err)
		return
	}
	statement, err := tx.PrepareContext(ctx, "SELECT CAST(:1 AS NUMBER(30,0)), NULL FROM dual")
	if err != nil {
		report("prepare", err)
		return
	}
	defer statement.Close()
	rows, err := statement.QueryContext(ctx, "9007199254740993")
	if err != nil {
		report("query", err)
		return
	}
	defer rows.Close()
	if _, err = rows.Columns(); err != nil {
		report("columns", err)
		return
	}
	for rows.Next() {
		var number, null sql.NullString
		if err = rows.Scan(&number, &null); err != nil {
			report("scan", err)
			return
		}
	}
	report("rows_complete", rows.Err())
	q := database.QueryRequest{SQL: "SELECT CAST(:1 AS NUMBER(30,0)), NULL FROM dual", Parameters: []any{json.Number("9007199254740993")}}
	if err = q.Validate(); err != nil {
		t.Fatal(err)
	}
	queryStart := time.Now()
	checks := 0
	_, executionErr := c.querySQLDriver(ctx, d, q, func(step context.Context) error {
		checks++
		t.Logf("Oracle synthetic authority_check=%d elapsed=%s context_done=%t", checks, time.Since(queryStart), step.Err() != nil)
		return step.Err()
	})
	t.Logf("Oracle synthetic executor_elapsed=%s error_type=%T authority_checks=%d context_done=%t", time.Since(queryStart), executionErr, checks, ctx.Err() != nil)
}
