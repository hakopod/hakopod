package cluster

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/hakopod/hakopod/internal/database"
	oranetwork "github.com/sijms/go-ora/v3/network"
	"io"
	"k8s.io/client-go/tools/clientcmd"
	"os"
	"strconv"
	"strings"
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
		t.Fatal("Oracle acceptance operation failed")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 18*time.Minute)
	defer cancel()
	d, observed := newOracleFixture(t, ctx, c, "")

	write := false
	read := func(sql string, args ...any) database.QueryResult {
		t.Helper()
		result, err := queryOracleSQLFixture(ctx, c, d, database.QueryRequest{SQL: sql, Parameters: args, ReadOnly: &write, ExecutionMode: "nontransactional"})
		if err != nil {
			oracleSafeQueryFailure(t, "adapter_read", err)
			t.Fatal("Oracle acceptance operation failed")
		}
		return result
	}
	result := read("SELECT CAST(:1 AS NUMBER(30,0)), NULL FROM dual", json.Number("9007199254740993"))
	if len(result.Rows) != 1 || len(result.Rows[0]) != 2 || result.Rows[0][0] == nil || *result.Rows[0][0] != "9007199254740993" || result.Rows[0][1] != nil {
		t.Fatal("numeric/null result differs")
	}
	_, err = queryOracleSQLFixture(ctx, c, d, database.QueryRequest{SQL: "CREATE TABLE hakopod_query_fixture_v2(value NUMBER(30,0) PRIMARY KEY)", ReadOnly: &write, ExecutionMode: "nontransactional"})
	if err != nil {
		t.Fatal("explicit schema write failed")
	}
	result, err = queryOracleSQLFixture(ctx, c, d, database.QueryRequest{SQL: "INSERT INTO hakopod_query_fixture_v2(value) VALUES (:1)", Parameters: []any{json.Number("9007199254740993")}, ReadOnly: &write, ExecutionMode: "nontransactional"})
	if err != nil || result.Outcome != "applied" {
		t.Fatal("DML was not applied")
	}
	result = read("SELECT value FROM hakopod_query_fixture_v2")
	if len(result.Rows) != 1 || len(result.Rows[0]) != 1 || result.Rows[0][0] == nil || *result.Rows[0][0] != "9007199254740993" {
		t.Fatal("write readback")
	}
	for _, statement := range []string{"INSERT INTO hakopod_query_fixture_v2 VALUES(2)", "DROP TABLE hakopod_query_fixture_v2", "COMMIT", "START TRANSACTION", "SET TRANSACTION READ WRITE", "CALL missing()"} {
		if _, err := queryOracleSQLFixture(ctx, c, d, database.QueryRequest{SQL: statement}); err == nil {
			t.Fatal("read-only/control statement accepted")
		}
	}
	limited := database.QueryRequest{SQL: "SELECT LEVEL FROM dual CONNECT BY LEVEL<=5", MaxRows: 2, ReadOnly: &write, ExecutionMode: "nontransactional"}
	limited.Validate()
	result, err = c.querySQLDriver(ctx, d, limited, func(context.Context) error { return nil })
	var rowLimitError *database.QueryError
	if !errors.As(err, &rowLimitError) || rowLimitError.Code != "database_query_result_limit" || rowLimitError.Outcome != "unknown" || len(result.Rows) != 2 || !result.Truncated {
		t.Fatal("row bound lacked a conservative nontransactional outcome")
	}
	smallLOB := read("SELECT XMLAGG(XMLELEMENT(e, RPAD('x',100,'x'))).GETCLOBVAL() FROM dual CONNECT BY LEVEL <= 2")
	if len(smallLOB.Rows) != 1 || len(smallLOB.Rows[0]) != 1 || smallLOB.Rows[0][0] == nil || len(*smallLOB.Rows[0][0]) < 200 {
		t.Fatal("Oracle CLOB positive control")
	}
	oversized := database.QueryRequest{SQL: "SELECT XMLAGG(XMLELEMENT(e, RPAD('x',4000,'x'))).GETCLOBVAL() FROM dual CONNECT BY LEVEL <= 600", ReadOnly: &write, ExecutionMode: "nontransactional"}
	if err = oversized.Validate(); err != nil {
		t.Fatal("Oracle acceptance operation failed")
	}
	_, err = c.querySQLDriver(ctx, d, oversized, func(context.Context) error { return nil })
	var oversizedError *database.QueryError
	if !errors.As(err, &oversizedError) || oversizedError.Code != "database_query_result_limit" || oversizedError.Outcome != "unknown" {
		t.Fatal("oversized Oracle value lacked conservative bounded outcome")
	}
	oracleCancellationAfterExecution(t, ctx, c, d, observed.Members[0])
	native, err := c.oracleApplicationConnection(ctx, d, observed.Members[0], true)
	if err != nil {
		t.Fatal("native connection")
	}
	defer native.Close()
	connection, err := native.Conn(ctx)
	if err != nil {
		t.Fatal("Oracle acceptance operation failed")
	}
	defer connection.Close()
	tx, err := connection.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal("Oracle acceptance operation failed")
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
		t.Fatal("Oracle acceptance operation failed")
	}
	var functionValue int
	if err = connection.QueryRowContext(ctx, "SELECT hakopod_mutate() FROM dual").Scan(&functionValue); err != nil || functionValue != 3 {
		t.Fatal("autonomous function positive control failed")
	}
	var before int
	if err = connection.QueryRowContext(ctx, "SELECT COUNT(*) FROM hakopod_query_fixture_v2").Scan(&before); err != nil || before != initialCount+1 {
		t.Fatal("Oracle acceptance operation failed")
	}
	tx, err = connection.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal("Oracle acceptance operation failed")
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, "SET TRANSACTION READ ONLY"); err != nil {
		t.Fatal("Oracle acceptance operation failed")
	}
	autonomousErr := tx.QueryRowContext(ctx, "SELECT hakopod_mutate() FROM dual").Scan(&functionValue)
	tx.Rollback()
	var after int
	if err = connection.QueryRowContext(ctx, "SELECT COUNT(*) FROM hakopod_query_fixture_v2").Scan(&after); err != nil || after != before+1 {
		t.Fatal("autonomous function persistence after parent rollback was not observed")
	}
	if autonomousErr != nil {
		t.Fatal("autonomous function in read-only parent failed")
	}
	if _, err = connection.ExecContext(ctx, "CREATE OR REPLACE FUNCTION hakopod_commit_error RETURN NUMBER IS PRAGMA AUTONOMOUS_TRANSACTION; BEGIN INSERT INTO hakopod_query_fixture_v2 VALUES ((SELECT NVL(MAX(value),0)+1 FROM hakopod_query_fixture_v2)); COMMIT; RAISE_APPLICATION_ERROR(-20001, 'control failure'); END;"); err != nil {
		t.Fatal("commit-then-error fixture function creation failed")
	}
	_, err = queryOracleSQLFixture(ctx, c, d, database.QueryRequest{SQL: "SELECT hakopod_commit_error() FROM dual", ReadOnly: &write, ExecutionMode: "nontransactional"})
	var commitError *database.QueryError
	if !errors.As(err, &commitError) || commitError.Outcome != "unknown" {
		t.Fatal("commit-then-error adapter outcome was not unknown")
	}
	var persisted int
	if connection.QueryRowContext(ctx, "SELECT COUNT(*) FROM hakopod_query_fixture_v2").Scan(&persisted) != nil || persisted != after+1 {
		t.Fatal("commit-then-error persistence was not observed")
	}
	t.Log("Oracle adapter numeric binds, explicit nontransactional writes and autonomous persistence controls passed")
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
	parent := ctx
	ctx, stopDiagnostic := context.WithTimeout(parent, 20*time.Second)
	defer stopDiagnostic()
	report := func(phase string, err error) {
		code := 0
		var native *oranetwork.OracleError
		if errors.As(err, &native) {
			code = native.ErrCode
		}
		t.Logf("Oracle synthetic phase=%s error_type=%T oracle_code=%d error_class=%s error_hash=%s elapsed=%s context_done=%t", phase, err, code, oracleDiagnosticErrorClass(err), oracleDiagnosticErrorHash(err), time.Since(started), ctx.Err() != nil)
	}
	observationStart := time.Now()
	observationContext, stopObservation := context.WithTimeout(parent, 20*time.Second)
	_, observationErr := c.ObserveDatabase(observationContext, d)
	t.Logf("Oracle synthetic observation_elapsed=%s error_type=%T error_class=%s error_hash=%s context_done=%t", time.Since(observationStart), observationErr, oracleDiagnosticErrorClass(observationErr), oracleDiagnosticErrorHash(observationErr), observationContext.Err() != nil)
	stopObservation()
	stopDiagnostic()
	ctx, stopDirect := context.WithTimeout(parent, 20*time.Second)
	defer stopDirect()
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
	rows.Close()
	statement.Close()
	tx.Rollback()
	conn.Close()
	client.Close()
	stopDirect()
	ctx, stopExecutor := context.WithTimeout(parent, 20*time.Second)
	defer stopExecutor()
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

func oracleDiagnosticErrorClass(err error) string {
	switch {
	case err == nil:
		return "none"
	case errors.Is(err, oranetwork.ErrReadLimit):
		return "receive_limit"
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline"
	case errors.Is(err, context.Canceled):
		return "cancelled"
	case errors.Is(err, driver.ErrBadConn):
		return "bad_connection"
	case errors.Is(err, io.EOF):
		return "eof"
	case errors.Is(err, io.ErrUnexpectedEOF):
		return "unexpected_eof"
	case errors.Is(err, sql.ErrConnDone):
		return "connection_done"
	case errors.Is(err, oranetwork.ErrConnReset):
		return "driver_context_reset"
	}
	// Compare only known static driver messages; never print backend text.
	switch err.Error() {
	case "attempt to set timeout on closed connection", "attempt to write on closed connection", "closed connection":
		return "closed_connection"
	case "TTC error: received code 3 during response reading":
		return "ttc_response_code_3"
	case "incorrect format for DBTimeZone":
		return "timezone_decode"
	default:
		return "unclassified"
	}
}

func oracleDiagnosticErrorHash(err error) string {
	if err == nil {
		return "none"
	}
	return fmt.Sprintf("%x", sha256.Sum256([]byte(err.Error())))
}

// This separate connection observes only a tiny synthetic value with default decoding.
func oracleDefaultDecoderPositiveControl(t *testing.T, parent context.Context, c *Client, d database.Resource, member database.Member) {
	t.Helper()
	ctx, cancel := context.WithTimeout(parent, 20*time.Second)
	defer cancel()
	native, err := c.oracleApplicationConnection(ctx, d, member, true)
	if err == nil {
		defer native.Close()
		var value int
		err = native.QueryRowContext(ctx, "SELECT 1 FROM dual").Scan(&value)
		if err == nil && value != 1 {
			t.Fatal("Oracle default decoder positive control differed")
		}
	}
	t.Logf("Oracle synthetic default_decoder error_type=%T error_class=%s error_hash=%s context_done=%t", err, oracleDiagnosticErrorClass(err), oracleDiagnosticErrorHash(err), ctx.Err() != nil)
}

func oracleCancellationAfterExecution(t *testing.T, parent context.Context, c *Client, d database.Resource, member database.Member) {
	t.Helper()
	ctx, cancel := context.WithTimeout(parent, 20*time.Second)
	defer cancel()
	native, err := c.oracleApplicationConnectionOptions(ctx, d, member, true, true)
	if err != nil {
		t.Fatal("Oracle cancellation connection")
	}
	defer native.Close()
	conn, err := native.Conn(ctx)
	if err != nil {
		t.Fatal("Oracle cancellation dedicated session")
	}
	defer conn.Close()
	if boundOracleQueryConnection(conn) != nil {
		t.Fatal("Oracle cancellation decoder bound")
	}
	var sid string
	if conn.QueryRowContext(ctx, "SELECT SYS_CONTEXT('USERENV','SID') FROM dual").Scan(&sid) != nil {
		t.Fatal("Oracle cancellation session identity")
	}
	sessionID, err := strconv.ParseUint(sid, 10, 32)
	if err != nil || sessionID == 0 {
		t.Fatal("Oracle cancellation session identifier invalid")
	}
	busyCtx, stop := context.WithCancel(ctx)
	defer stop()
	write := false
	q := database.QueryRequest{SQL: "SELECT /* hakopod_cancel_control */ SUM(LEVEL) FROM dual CONNECT BY LEVEL<=1000000000", ReadOnly: &write, ExecutionMode: "nontransactional"}
	if q.Validate() != nil {
		t.Fatal("Oracle cancellation request")
	}
	ended := make(chan error, 1)
	go func() {
		_, err := runSQLDriverQuery(busyCtx, conn, "oracle", q, "read", func(context.Context) error { return nil })
		ended <- err
	}()
	started := false
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline) && ctx.Err() == nil; {
		select {
		case <-ended:
			t.Fatal("Oracle cancellation query ended before execution proof")
		default:
		}
		check, stopCheck := context.WithTimeout(ctx, 2*time.Second)
		value, e := c.oracleLocalQuery(check, d, member, fmt.Sprintf("SELECT COUNT(*) FROM v$session s WHERE s.sid=%d AND s.username='APP' AND s.status='ACTIVE' AND EXISTS (SELECT 1 FROM v$sql q WHERE q.sql_id=s.sql_id AND q.sql_text LIKE 'SELECT /* hakopod_cancel_control */%%')", sessionID))
		stopCheck()
		if e != nil {
			t.Fatal("Oracle cancellation execution observation")
		}
		if strings.TrimSpace(value) == "1" {
			started = true
			break
		}
		if sleepContext(ctx, 50*time.Millisecond) != nil {
			break
		}
	}
	if !started {
		t.Fatal("Oracle server execution was not observed before cancellation")
	}
	select {
	case <-ended:
		t.Fatal("Oracle cancellation query ended before cancellation")
	default:
	}
	began := time.Now()
	stop()
	select {
	case err := <-ended:
		var queryError *database.QueryError
		if !errors.As(err, &queryError) || queryError.Outcome != "unknown" {
			t.Fatal("Oracle in-flight cancellation lacked a conservative query outcome")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Oracle in-flight cancellation exceeded five seconds")
	}
	t.Logf("Oracle cancellation after server execution returned in %s", time.Since(began))
}

// Only static categories are reported. Backend text and result bodies stay private.
func oracleSafeQueryFailure(t *testing.T, phase string, err error) {
	t.Helper()
	code, outcome := "unclassified", "unclassified"
	var failure *database.QueryError
	if errors.As(err, &failure) {
		switch failure.Code {
		case "database_query_unavailable", "database_query_failed", "database_query_statement_unsupported", "database_query_result_limit", "database_query_authority_changed", "database_query_read_only_unsupported", "database_query_execution_mode_unsupported":
			code = failure.Code
		}
		switch failure.Outcome {
		case "not_started", "unknown", "rolled_back":
			outcome = failure.Outcome
		}
	}
	kind, oracleCode := "other", "none"
	var native *oranetwork.OracleError
	if err == nil {
		kind = "none"
	} else if errors.As(err, &failure) {
		kind = "query_error"
	} else if errors.As(err, &native) {
		kind = "oracle_error"
		switch native.ErrCode {
		case 1013, 14551, 14552, 20001, 6550, 904, 942:
			oracleCode = strconv.Itoa(native.ErrCode)
		default:
			oracleCode = "other"
		}
	}
	t.Logf("Oracle synthetic phase=%s type=%s class=%s code=%s oracle_code=%s outcome=%s", phase, kind, oracleDiagnosticErrorClass(err), code, oracleCode, outcome)
}

func TestManagedOracleIsolatedQueryDiagnosticLive(t *testing.T) {
	if os.Getenv("HAKOPOD_ORACLE_ISOLATED_QUERY_TEST") != "1" {
		t.Skip("set HAKOPOD_ORACLE_ISOLATED_QUERY_TEST=1 for owned development diagnostics")
	}
	if os.Getenv("HAKOPOD_KEEP_DATABASE_FIXTURES") != "" || os.Getenv("HAKOPOD_ORACLE_FIXTURE_ID") != "" {
		t.Fatal("diagnostics require a fresh fixture and normal cleanup")
	}
	path := os.Getenv("HAKOPOD_TEST_KUBECONFIG")
	config, err := clientcmd.LoadFromFile(path)
	if err != nil || config.CurrentContext != "k3d-hakopod-dev" {
		t.Fatal("diagnostic development context")
	}
	c, err := New(path, developmentDatabaseOptions(t))
	if err != nil {
		t.Fatal("diagnostic client")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 18*time.Minute)
	defer cancel()
	d, observed := newOracleFixture(t, ctx, c, "")
	if len(observed.Members) != 1 || observed.Members[0].UID == "" || !observed.Members[0].Ready {
		t.Fatal("diagnostic requires one ready owned Oracle member")
	}
	if _, _, err = c.databaseExecTarget(ctx, d, observed.Members[0]); err != nil {
		t.Fatal("diagnostic member ownership validation failed")
	}
	write := false
	for _, probe := range []struct {
		name, statement string
		args            []any
	}{
		{"literal_number", "SELECT CAST(1 AS NUMBER(30,0)) FROM dual", nil},
		{"untyped_null", "SELECT NULL FROM dual", nil},
		{"typed_null", "SELECT CAST(NULL AS NUMBER(30,0)) FROM dual", nil},
		{"bound_number", "SELECT CAST(:1 AS NUMBER(30,0)) FROM dual", []any{json.Number("9007199254740993")}},
		{"bound_number_null", "SELECT CAST(:1 AS NUMBER(30,0)), NULL FROM dual", []any{json.Number("9007199254740993")}},
	} {
		q := database.QueryRequest{SQL: probe.statement, Parameters: probe.args, ReadOnly: &write, ExecutionMode: "nontransactional"}
		if q.Validate() != nil {
			t.Fatal("diagnostic request invalid")
		}
		_, adapterErr := queryOracleSQLFixture(ctx, c, d, q)
		oracleSafeQueryFailure(t, probe.name+"_adapter", adapterErr)
		step, stop := context.WithTimeout(ctx, 20*time.Second)
		native, e := c.oracleApplicationConnectionOptions(step, d, observed.Members[0], true, true)
		phase := "connect"
		if e == nil {
			var conn *sql.Conn
			conn, e = native.Conn(step)
			phase = "connection"
			if e == nil {
				phase = "decoder_bound"
				e = boundOracleQueryConnection(conn)
				if e == nil {
					phase, e = oracleIsolatedDirectQuery(step, conn, q)
				}
				conn.Close()
			}
			native.Close()
		}
		stop()
		oracleSafeQueryFailure(t, probe.name+"_direct_"+phase, e)
	}
}

func oracleIsolatedDirectQuery(ctx context.Context, conn *sql.Conn, q database.QueryRequest) (string, error) {
	statement, err := conn.PrepareContext(ctx, q.SQL)
	if err != nil {
		return "prepare", err
	}
	defer statement.Close()
	args := make([]any, len(q.Parameters))
	for i, value := range q.Parameters {
		if number, ok := value.(json.Number); ok {
			args[i] = string(number)
		} else {
			args[i] = value
		}
	}
	rows, err := statement.QueryContext(ctx, args...)
	if err != nil {
		return "query", err
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		return "columns", err
	}
	if len(columns) < 1 || len(columns) > 2 {
		return "column_width", errors.New("diagnostic column width")
	}
	count := 0
	for rows.Next() {
		count++
		if count > 1 {
			return "row_bound", errors.New("diagnostic row bound")
		}
		values := make([]sql.NullString, len(columns))
		targets := make([]any, len(columns))
		for i := range values {
			targets[i] = &values[i]
		}
		if err = rows.Scan(targets...); err != nil {
			return "scan", err
		}
	}
	if err = rows.Err(); err != nil {
		return "rows", err
	}
	if count != 1 {
		return "row_count", errors.New("diagnostic row count")
	}
	return "complete", nil
}
