package cluster

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/hakopod/hakopod/internal/database"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"reflect"
	"sort"
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
	request := database.QueryRequest{SQL: "SELECT SLEEP(15), 'hakopod_vitess_started_probe'"}
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
			var problem *database.QueryError
			if errors.As(earlyErr, &problem) {
				t.Fatalf("query finished before observed execution: code=%s outcome=%s", problem.Code, problem.Outcome)
			}
			t.Fatalf("query finished before observed execution: error_present=%t", earlyErr != nil)
		default:
		}
		primaries, primaryErr := vitessObservedPrimaries(d, observed)
		if primaryErr != nil || len(primaries) != 1 {
			t.Fatal("primary tablet unavailable")
		}
		tablet := primaries[0]
		out := &databaseBoundedWriter{limit: 1024}
		probeCtx, probeCancel := context.WithTimeout(ctx, 2*time.Second)
		probeErr := c.DatabaseExec(probeCtx, d, tablet, vitessLocalCommand("vt_dba", "SELECT COUNT(*) FROM information_schema.PROCESSLIST WHERE ID<>CONNECTION_ID() AND COMMAND='Query' AND INFO LIKE '%hakopod_vitess_started_probe%'"), nil, out)
		probeContextDone := probeCtx.Err() != nil
		probeCancel()
		if probeErr != nil {
			category := "other"
			if probeContextDone {
				category = "context_done"
			}
			t.Logf("Vitess synthetic observer category=%s context_done=%t", category, probeContextDone)
			t.Fatal("tablet execution observer unavailable")
		}
		started = strings.TrimSpace(out.String()) == "1"
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
	vitessQueryPhaseDiagnostic(t, ctx, c, d, observed, password)
	write := false
	query := func(statement string, parameters ...any) (database.QueryResult, error) {
		return queryVitessSQLFixture(ctx, c, d, database.QueryRequest{SQL: statement, Parameters: parameters, ReadOnly: &write})
	}
	schema := database.QueryRequest{SQL: "CREATE TABLE records (id BIGINT NOT NULL PRIMARY KEY, payload VARBINARY(16) NOT NULL, label VARCHAR(80) CHARACTER SET utf8mb4 NOT NULL)", ReadOnly: &write, ExecutionMode: "nontransactional"}
	schemaStarted := time.Now()
	_, schemaErr := queryVitessSQLFixture(ctx, c, d, schema)
	logVitessQueryPhase(t, ctx, "schema_adapter", schemaStarted, schemaErr)
	if schemaErr != nil {
		t.Fatal("sharded schema", schemaErr)
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
	snapshot := func() database.QueryResult {
		t.Helper()
		r, e := queryVitessSQLFixture(ctx, c, d, database.QueryRequest{SQL: "SELECT id,payload,label FROM records ORDER BY id"})
		if e != nil || len(r.Rows) != 32 {
			t.Fatal("complete shard snapshot failed")
		}
		return r
	}
	before := snapshot()
	for _, mode := range []string{"transaction", "nontransactional"} {
		_, e := queryVitessSQLFixture(ctx, c, d, database.QueryRequest{SQL: "UPDATE records SET label=?", Parameters: []any{"rejected scatter"}, ReadOnly: &write, ExecutionMode: mode})
		var failure *database.QueryError
		wantOutcome := "unknown"
		if mode == "transaction" {
			wantOutcome = "rolled_back"
		}
		if !errors.As(e, &failure) || failure.Code != "database_query_failed" || failure.Outcome != wantOutcome {
			t.Fatal("cross-shard DML lacked server rejection outcome", mode)
		}
		if !reflect.DeepEqual(before.Rows, snapshot().Rows) {
			t.Fatal("rejected cross-shard DML changed data", mode)
		}
	}
	one, e := query("UPDATE records SET label=? WHERE id=?", "single-shard committed", json.Number("1"))
	if e != nil || one.Outcome != "committed" || one.RowsAffected != 1 {
		t.Fatal("single-shard commit failed")
	}
	afterCommit := snapshot()
	expectedCommit := snapshotRowsWithLabel(before.Rows, "1", "single-shard committed")
	if !reflect.DeepEqual(expectedCommit, afterCommit.Rows) {
		t.Fatal("single-shard commit changed unexpected data")
	}
	revoked := database.QueryRequest{SQL: "UPDATE records SET label=? WHERE id=?", Parameters: []any{"revoked", json.Number("1")}, ReadOnly: &write}
	revoked.Validate()
	calls := 0
	_, e = c.querySQLDriver(ctx, d, revoked, func(context.Context) error {
		calls++
		if calls >= 3 {
			return context.Canceled
		}
		return nil
	})
	var denied *database.QueryError
	if !errors.As(e, &denied) || denied.Code != "database_query_authority_changed" || denied.Outcome != "rolled_back" || calls != 3 {
		t.Fatal("single-shard revoke failed")
	}
	if !reflect.DeepEqual(afterCommit.Rows, snapshot().Rows) {
		t.Fatal("revoked single-shard write changed data")
	}
	applied, e := queryVitessSQLFixture(ctx, c, d, database.QueryRequest{SQL: "UPDATE records SET label=? WHERE id=?", Parameters: []any{"single-shard applied", json.Number("1")}, ReadOnly: &write, ExecutionMode: "nontransactional"})
	if e != nil || applied.Outcome != "applied" || applied.RowsAffected != 1 {
		t.Fatal("single-shard nontransactional write failed")
	}
	final := snapshot()
	if !reflect.DeepEqual(snapshotRowsWithLabel(afterCommit.Rows, "1", "single-shard applied"), final.Rows) {
		t.Fatal("single-shard applied write changed unexpected data")
	}
	limit := database.QueryRequest{SQL: "SELECT id FROM records ORDER BY id", MaxRows: 2}
	r, err = queryVitessSQLFixture(ctx, c, d, limit)
	if err != nil || len(r.Rows) != 2 || !r.Truncated {
		t.Fatal("sharded limit", err)
	}
	t.Log("Two-shard Vitess reads, single-shard writes and rejected cross-shard persistence controls passed")
}

func snapshotRowsWithLabel(rows [][]*string, id, label string) [][]*string {
	expected := make([][]*string, len(rows))
	for i, row := range rows {
		expected[i] = append([]*string(nil), row...)
		if len(row) == 3 && row[0] != nil && *row[0] == id {
			value := label
			expected[i][2] = &value
		}
	}
	return expected
}

// Diagnostics use only a fixed synthetic SELECT and never print SQL, result
// bodies, credentials or backend error text. Each connection path retains the
// same twenty-second bound as the production query adapter.
func vitessQueryPhaseDiagnostic(t *testing.T, ctx context.Context, c *Client, d database.Resource, observed database.Observation, password []byte) {
	t.Helper()
	specCtx, stopSpec := context.WithTimeout(ctx, 20*time.Second)
	expected, expectedErr := c.databaseObject(specCtx, d)
	if expectedErr == nil {
		expectedErr = c.applyVitessIdentity(specCtx, d, expected)
	}
	live, liveErr := c.dynamic.Resource(vitessDatabaseResource).Namespace(DatabaseNamespace(d.ID)).Get(specCtx, "database", metav1.GetOptions{})
	if expectedErr == nil && liveErr == nil {
		paths := vitessQuerySpecDifferencePaths(expected.Object["spec"], live.Object["spec"], "spec", 64)
		t.Logf("Vitess synthetic controller_spec_diff_count=%d paths=%v", len(paths), paths)
	} else {
		t.Logf("Vitess synthetic controller_spec_compare expected_error_type=%T live_error_type=%T", expectedErr, liveErr)
	}
	stopSpec()
	observationCtx, stopObservation := context.WithTimeout(ctx, 20*time.Second)
	started := time.Now()
	current, err := c.ObserveDatabase(observationCtx, d)
	logVitessQueryPhase(t, observationCtx, "observe", started, err)
	t.Logf("Vitess synthetic observation ready=%t members=%d routing_present=%t", current.Status == "ready", len(current.Members), current.Routing != nil)
	stopObservation()
	if observed.Routing == nil || len(observed.Routing.Members) == 0 {
		return
	}
	directCtx, stopDirect := context.WithTimeout(ctx, 20*time.Second)
	defer stopDirect()
	started = time.Now()
	trust, err := c.DatabaseTrust(directCtx, d)
	logVitessQueryPhase(t, directCtx, "trust", started, err)
	if err != nil {
		return
	}
	started = time.Now()
	identity, err := redisTLSConfig(trust, "database."+DatabaseNamespace(d.ID)+".svc")
	logVitessQueryPhase(t, directCtx, "tls_config", started, err)
	if err != nil {
		return
	}
	started = time.Now()
	client, err := c.vitessGatewayClientWithReadTimeout(directCtx, d, observed.Routing.Members[0], "app@primary", password, identity, 20*time.Second)
	logVitessQueryPhase(t, directCtx, "connector", started, err)
	if err != nil {
		return
	}
	defer client.Close()
	started = time.Now()
	connection, err := client.Conn(directCtx)
	logVitessQueryPhase(t, directCtx, "connect", started, err)
	if err != nil {
		return
	}
	defer connection.Close()
	q := database.QueryRequest{SQL: "SELECT 1"}
	if err = q.Validate(); err != nil {
		t.Fatal(err)
	}
	checks := 0
	started = time.Now()
	_, err = runSQLDriverQuery(directCtx, connection, "vitess", q, "read", func(step context.Context) error {
		checks++
		t.Logf("Vitess synthetic authority_check=%d elapsed=%s context_done=%t", checks, time.Since(started), step.Err() != nil)
		return step.Err()
	})
	logVitessQueryPhase(t, directCtx, "direct_executor", started, err)
	adapterCtx, stopAdapter := context.WithTimeout(ctx, 20*time.Second)
	defer stopAdapter()
	started = time.Now()
	_, err = queryVitessSQLFixture(adapterCtx, c, d, q)
	logVitessQueryPhase(t, adapterCtx, "read_adapter", started, err)
}

func logVitessQueryPhase(t *testing.T, ctx context.Context, phase string, started time.Time, err error) {
	t.Helper()
	code, outcome := "", ""
	var problem *database.QueryError
	if errors.As(err, &problem) {
		code, outcome = problem.Code, problem.Outcome
	}
	t.Logf("Vitess synthetic phase=%s elapsed=%s error_type=%T code=%s outcome=%s context_done=%t", phase, time.Since(started), err, code, outcome, ctx.Err() != nil)
}

// Report field names only; controller values can include private configuration.
func vitessQuerySpecDifferencePaths(expected, live any, path string, limit int) []string {
	if limit <= 0 || reflect.DeepEqual(expected, live) {
		return nil
	}
	if len(path) > 240 || strings.Count(path, ".")+strings.Count(path, "[") > 16 {
		return []string{"<nested-difference>"}
	}
	result := []string{}
	if want, ok := expected.(map[string]any); ok {
		actual, ok := live.(map[string]any)
		if !ok {
			return []string{path}
		}
		keys := map[string]bool{}
		for key := range want {
			keys[key] = true
		}
		for key := range actual {
			keys[key] = true
		}
		ordered := []string{}
		for key := range keys {
			ordered = append(ordered, key)
		}
		sort.Strings(ordered)
		seenExtra := false
		for _, key := range ordered {
			if len(result) >= limit {
				break
			}
			w, wok := want[key]
			a, aok := actual[key]
			if !wok {
				if !seenExtra {
					result = append(result, path+".<extra>")
					seenExtra = true
				}
				continue
			}
			if !aok {
				result = append(result, path+"."+key)
				continue
			}
			result = append(result, vitessQuerySpecDifferencePaths(w, a, path+"."+key, limit-len(result))...)
		}
		return result
	}
	if want, ok := expected.([]any); ok {
		actual, ok := live.([]any)
		if !ok || len(want) != len(actual) {
			return []string{path}
		}
		for i := range want {
			if len(result) >= limit {
				break
			}
			result = append(result, vitessQuerySpecDifferencePaths(want[i], actual[i], fmt.Sprintf("%s[%d]", path, i), limit-len(result))...)
		}
		return result
	}
	return []string{path}
}
