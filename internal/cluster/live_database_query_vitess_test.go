package cluster

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/hakopod/hakopod/internal/database"
	"strings"
	"testing"
	"time"
)

func TestManagedVitessQueryLive(t *testing.T) {
	fixtures, ctx := newVitessLiveClient(t, 12*time.Minute)
	c := fixtures.c
	d, password := newVitessFixture(t, ctx, fixtures, "standalone", 1)
	observed := waitVitessFixture(t, ctx, c, d, password)

	read := func(sql string, args ...any) database.QueryResult {
		t.Helper()
		result, err := queryVitessSQLFixture(ctx, c, d, database.QueryRequest{SQL: sql, Parameters: args})
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	result := read("SELECT CAST(? AS DECIMAL(30,0)), NULL", json.Number("9007199254740993"))
	if len(result.Rows) != 1 || result.Rows[0][0] == nil || *result.Rows[0][0] != "9007199254740993" || result.Rows[0][1] != nil {
		t.Fatalf("numeric/null result differs: %#v", result)
	}
	var err error
	write := false
	_, err = queryVitessSQLFixture(ctx, c, d, database.QueryRequest{SQL: "CREATE TABLE IF NOT EXISTS hakopod_query_fixture_v2(value DECIMAL(30,0) PRIMARY KEY)", ReadOnly: &write, ExecutionMode: "nontransactional"})
	if err != nil {
		t.Fatal("explicit schema write", err)
	}
	result, err = queryVitessSQLFixture(ctx, c, d, database.QueryRequest{SQL: "INSERT INTO hakopod_query_fixture_v2(value) VALUES (?)", Parameters: []any{json.Number("9007199254740993")}, ReadOnly: &write})
	if err != nil || result.Outcome != "committed" {
		t.Fatal("DML commit", err, result.Outcome)
	}
	result = read("SELECT value FROM hakopod_query_fixture_v2")
	if len(result.Rows) != 1 || result.Rows[0][0] == nil || *result.Rows[0][0] != "9007199254740993" {
		t.Fatal("write readback")
	}
	for _, statement := range []string{"INSERT INTO hakopod_query_fixture_v2 VALUES(2)", "DROP TABLE hakopod_query_fixture_v2", "COMMIT", "START TRANSACTION", "SET TRANSACTION READ WRITE", "CALL missing()"} {
		if _, err := queryVitessSQLFixture(ctx, c, d, database.QueryRequest{SQL: statement}); err == nil {
			t.Fatalf("read-only/control statement accepted: %s", statement)
		}
	}

	trust, e := c.DatabaseTrust(ctx, d)
	if e != nil {
		t.Fatal(e)
	}
	identity, e := redisTLSConfig(trust, "database."+DatabaseNamespace(d.ID)+".svc")
	if e != nil {
		t.Fatal(e)
	}
	native, e := c.vitessGatewayClientWithReadTimeout(ctx, d, observed.Routing.Members[0], "app@primary", password, identity, 20*time.Second)
	if e != nil {
		t.Fatal(e)
	}
	defer native.Close()
	cancelCtx, cancelQuery := context.WithCancel(ctx)
	defer cancelQuery()
	cancelClient, e := c.vitessGatewayClientWithReadTimeout(cancelCtx, d, observed.Routing.Members[0], "app@primary", password, identity, 20*time.Second)
	if e != nil {
		t.Fatal("cancellation connection unavailable")
	}
	defer cancelClient.Close()
	cancelConnection, e := cancelClient.Conn(cancelCtx)
	if e != nil {
		t.Fatal("cancellation session unavailable")
	}
	defer cancelConnection.Close()
	var connectionID int64
	if e = cancelConnection.QueryRowContext(ctx, "SELECT CONNECTION_ID()").Scan(&connectionID); e != nil {
		t.Fatal("capture cancellation connection unavailable")
	}
	request := database.QueryRequest{SQL: "SELECT SLEEP(15)"}
	if e = request.Validate(); e != nil {
		t.Fatal(e)
	}
	cancelled := make(chan error, 1)
	go func() {
		_, executionErr := runSQLDriverQuery(cancelCtx, cancelConnection, "vitess", request, "read", func(context.Context) error { return nil })
		cancelled <- executionErr
	}()
	started := false
	for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); {
		select {
		case earlyErr := <-cancelled:
			t.Fatalf("query finished before observed execution: error_type=%T", earlyErr)
		default:
		}
		processes, probeErr := native.QueryContext(ctx, "SHOW FULL PROCESSLIST")
		if probeErr != nil {
			t.Fatal("execution observer unavailable")
		}
		columns, probeErr := processes.Columns()
		if probeErr != nil {
			processes.Close()
			t.Fatal("execution columns unavailable")
		}
		for processes.Next() {
			values := make([]sql.NullString, len(columns))
			args := make([]any, len(columns))
			for i := range values {
				args[i] = &values[i]
			}
			if probeErr = processes.Scan(args...); probeErr != nil {
				processes.Close()
				t.Fatal("execution scan unavailable")
			}
			matches, executing := false, false
			for i, column := range columns {
				if strings.EqualFold(column, "id") && values[i].Valid && values[i].String == fmt.Sprint(connectionID) {
					matches = true
				}
				if strings.EqualFold(column, "info") && values[i].Valid && strings.Contains(strings.ToUpper(values[i].String), "SLEEP") {
					executing = true
				}
			}
			if matches && executing {
				started = true
			}
		}
		probeErr = processes.Err()
		processes.Close()
		if probeErr != nil {
			t.Fatal("execution observer stream unavailable")
		}
		if started {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !started {
		t.Fatal("dedicated query connection never reached observed server execution")
	}
	cancelQuery()
	select {
	case e = <-cancelled:
		if e == nil {
			t.Fatal("in-flight cancellation returned success")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("in-flight cancellation exceeded five seconds")
	}

	connection, e := native.Conn(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer connection.Close()
	var before int
	if e = connection.QueryRowContext(ctx, "SELECT COUNT(*) FROM hakopod_query_fixture_v2").Scan(&before); e != nil || before != 1 {
		t.Fatal("baseline count", e)
	}
	tx, e := connection.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if e != nil {
		t.Fatal("readonly transaction", e)
	}
	if _, e = tx.ExecContext(ctx, "INSERT INTO hakopod_query_fixture_v2 VALUES(2)"); e == nil {
		tx.Rollback()
		t.Fatal("server readonly accepted write")
	}
	tx.Rollback()
	var after int
	if e = connection.QueryRowContext(ctx, "SELECT COUNT(*) FROM hakopod_query_fixture_v2").Scan(&after); e != nil {
		t.Fatal("count readback", e)
	}
	if after != before {
		t.Fatal("readonly persisted mutation")
	}
	q := database.QueryRequest{SQL: "INSERT INTO hakopod_query_fixture_v2 VALUES(3)", ReadOnly: &write}
	q.Validate()
	calls := 0
	_, e = c.querySQLDriver(ctx, d, q, func(context.Context) error {
		calls++
		if calls >= 3 {
			return context.Canceled
		}
		return nil
	})
	var revoked *database.QueryError
	if !errors.As(e, &revoked) || revoked.Code != "database_query_authority_changed" || revoked.Outcome != "rolled_back" || calls != 3 {
		t.Fatal("authority loss outcome", e, calls)
	}
	if e = connection.QueryRowContext(ctx, "SELECT COUNT(*) FROM hakopod_query_fixture_v2").Scan(&after); e != nil {
		t.Fatal("count readback", e)
	}
	if after != before {
		t.Fatal("revoked write persisted")
	}
	for _, limited := range []database.QueryRequest{{SQL: "SELECT 1 UNION ALL SELECT 2", MaxRows: 1}, {SQL: "SELECT REPEAT('x',2000)", MaxBytes: 1024}} {
		limited.Validate()
		r, e := c.querySQLDriver(ctx, d, limited, func(context.Context) error { return nil })
		if e != nil {
			var bounded *database.QueryError
			if !errors.As(e, &bounded) || bounded.Code != "database_query_result_limit" {
				t.Fatal("unexpected bound failure", e)
			}
		} else if !r.Truncated || len(r.Rows) > limited.MaxRows {
			t.Fatal("result bound not enforced")
		}
	}
	t.Log("Vitess typed binds, writes, readback and cancellation completed")
}

func queryVitessSQLFixture(ctx context.Context, c *Client, d database.Resource, q database.QueryRequest) (database.QueryResult, error) {
	if err := q.Validate(); err != nil {
		return database.QueryResult{}, err
	}
	return c.querySQLDriver(ctx, d, q, func(context.Context) error { return nil })
}

func TestManagedVitessShardedQueryLive(t *testing.T) {
	fixtures, ctx := newVitessLiveClient(t, 18*time.Minute)
	c := fixtures.c
	d, password := newVitessFixture(t, ctx, fixtures, "cluster", 2)
	observed := waitVitessFixture(t, ctx, c, d, password)
	write := false
	query := func(statement string, parameters ...any) (database.QueryResult, error) {
		return queryVitessSQLFixture(ctx, c, d, database.QueryRequest{SQL: statement, Parameters: parameters, ReadOnly: &write})
	}
	schema := database.QueryRequest{SQL: "CREATE TABLE records (id BIGINT NOT NULL PRIMARY KEY, payload VARBINARY(16) NOT NULL, label VARCHAR(80) CHARACTER SET utf8mb4 NOT NULL)", ReadOnly: &write, ExecutionMode: "nontransactional"}
	if _, err := queryVitessSQLFixture(ctx, c, d, schema); err != nil {
		t.Fatal("sharded schema", err)
	}
	for i := 1; i <= 32; i++ {
		r, err := query("INSERT INTO records(id,payload,label) VALUES (?,?,?)", json.Number(fmt.Sprint(i)), "payload", "नमस्ते / 東京")
		if err != nil || r.Outcome != "committed" {
			t.Fatal("sharded bind", i, err)
		}
	}
	read := database.QueryRequest{SQL: "SELECT COUNT(*) FROM records"}
	r, err := queryVitessSQLFixture(ctx, c, d, read)
	if err != nil || len(r.Rows) != 1 || len(r.Rows[0]) != 1 || r.Rows[0][0] == nil || *r.Rows[0][0] != "32" {
		t.Fatal("sharded readback", err)
	}
	observed, err = c.ObserveDatabase(ctx, d)
	if err != nil {
		t.Fatal(err)
	}
	checkVitessShardRouting(t, ctx, c, d, observed)
	cross, err := query("UPDATE records SET label=?", "cross-shard committed")
	if err != nil || cross.Outcome != "committed" || cross.RowsAffected != 32 {
		t.Fatal("cross-shard commit", err, cross.Outcome, cross.RowsAffected)
	}
	verifyLabels := func(expected string) {
		t.Helper()
		check, e := queryVitessSQLFixture(ctx, c, d, database.QueryRequest{SQL: "SELECT COUNT(*) FROM records WHERE label=?", Parameters: []any{expected}})
		if e != nil || len(check.Rows) != 1 || len(check.Rows[0]) != 1 || check.Rows[0][0] == nil || *check.Rows[0][0] != "32" {
			t.Fatal("cross-shard label readback", e)
		}
	}
	verifyLabels("cross-shard committed")
	revoked := database.QueryRequest{SQL: "UPDATE records SET label='revoked'", ReadOnly: &write}
	revoked.Validate()
	calls := 0
	_, err = c.querySQLDriver(ctx, d, revoked, func(context.Context) error {
		calls++
		if calls >= 3 {
			return context.Canceled
		}
		return nil
	})
	var denied *database.QueryError
	if !errors.As(err, &denied) || denied.Code != "database_query_authority_changed" || denied.Outcome != "rolled_back" || calls != 3 {
		t.Fatal("sharded revoke", err, calls)
	}
	r, err = queryVitessSQLFixture(ctx, c, d, read)
	if err != nil || len(r.Rows) != 1 || r.Rows[0][0] == nil || *r.Rows[0][0] != "32" {
		t.Fatal("sharded revoke persisted", err)
	}
	verifyLabels("cross-shard committed")
	nontransactional := database.QueryRequest{SQL: "UPDATE records SET label=?", Parameters: []any{"cross-shard applied"}, ReadOnly: &write, ExecutionMode: "nontransactional"}
	applied, e := queryVitessSQLFixture(ctx, c, d, nontransactional)
	if e != nil || applied.Outcome != "applied" || applied.RowsAffected != 32 {
		t.Fatal("cross-shard nontransactional outcome", e, applied.Outcome, applied.RowsAffected)
	}
	verifyLabels("cross-shard applied")
	limit := database.QueryRequest{SQL: "SELECT id FROM records ORDER BY id", MaxRows: 2}
	r, err = queryVitessSQLFixture(ctx, c, d, limit)
	if err != nil || len(r.Rows) != 2 || !r.Truncated {
		t.Fatal("sharded limit", err)
	}
	t.Log("Two-shard Vitess routing, bound writes, rollback and bounded results passed")
}
