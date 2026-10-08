package cluster

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"github.com/hakopod/hakopod/internal/database"
	"io"
	"testing"
)

type queryRollbackConnector struct {
	fail     bool
	execFail bool
	begins   *int
}

func (c queryRollbackConnector) Connect(context.Context) (driver.Conn, error) {
	return queryRollbackConn{fail: c.fail, execFail: c.execFail, begins: c.begins}, nil
}
func (c queryRollbackConnector) Driver() driver.Driver { return queryRollbackDriver{} }

type queryRollbackDriver struct{}

func (queryRollbackDriver) Open(string) (driver.Conn, error) {
	return nil, errors.New("connector required")
}

type queryRollbackConn struct {
	fail     bool
	execFail bool
	begins   *int
}

func (c queryRollbackConn) Prepare(string) (driver.Stmt, error) {
	return queryRollbackStmt{fail: c.execFail}, nil
}
func (c queryRollbackConn) ExecContext(ctx context.Context, statement string, args []driver.NamedValue) (driver.Result, error) {
	if statement != "ROLLBACK" {
		return nil, driver.ErrSkip
	}
	if c.fail {
		return nil, errors.New("rollback disconnected")
	}
	return driver.RowsAffected(0), nil
}
func (c queryRollbackConn) QueryContext(ctx context.Context, statement string, args []driver.NamedValue) (driver.Rows, error) {
	if statement != "SHOW COUNT(*) WARNINGS" {
		return nil, driver.ErrSkip
	}
	return &mysqlRollbackCountRows{count: 0}, nil
}
func (c queryRollbackConn) Close() error { return nil }
func (c queryRollbackConn) Begin() (driver.Tx, error) {
	if c.begins != nil {
		*c.begins++
	}
	return queryRollbackTx{fail: c.fail}, nil
}

type queryRollbackTx struct{ fail bool }

func (queryRollbackTx) Commit() error { return nil }
func (tx queryRollbackTx) Rollback() error {
	if tx.fail {
		return errors.New("rollback disconnected")
	}
	return nil
}

func TestSQLRollbackOutcomeRequiresConfirmation(t *testing.T) {
	for _, fail := range []bool{false, true} {
		db := sql.OpenDB(queryRollbackConnector{fail: fail})
		tx, err := db.BeginTx(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		want := "rolled_back"
		if fail {
			want = "unknown"
		}
		if got := sqlRollbackOutcome(tx); got != want {
			t.Fatalf("failed rollback=%v: got %s want %s", fail, got, want)
		}
		if got := sqlRollbackOutcome(tx); got != "unknown" {
			t.Fatal("already completed transaction claimed confirmed rollback")
		}
		db.Close()
	}
	if sqlRollbackOutcome(nil) != "unknown" {
		t.Fatal("nontransactional execution claimed rollback")
	}
}

type queryRollbackStmt struct{ fail bool }

func (queryRollbackStmt) Close() error  { return nil }
func (queryRollbackStmt) NumInput() int { return 0 }
func (stmt queryRollbackStmt) Exec([]driver.Value) (driver.Result, error) {
	if stmt.fail {
		return nil, context.Canceled
	}
	return driver.RowsAffected(1), nil
}
func (queryRollbackStmt) Query([]driver.Value) (driver.Rows, error) {
	return &queryRollbackRows{}, nil
}

type queryRollbackRows struct{ returned bool }

func (*queryRollbackRows) Columns() []string { return []string{"value"} }
func (*queryRollbackRows) Close() error      { return nil }
func (r *queryRollbackRows) Next(values []driver.Value) error {
	if r.returned {
		return io.EOF
	}
	r.returned = true
	values[0] = "1"
	return nil
}

func TestSQLDriverWriteQueryRevocationReportsRollback(t *testing.T) {
	for _, rollbackFail := range []bool{false, true} {
		db := sql.OpenDB(queryRollbackConnector{fail: rollbackFail})
		conn, err := db.Conn(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		write := false
		q := database.QueryRequest{SQL: "SELECT mutate_fixture()", ReadOnly: &write}
		if err := q.Validate(); err != nil {
			t.Fatal(err)
		}
		calls := 0
		check := func(context.Context) error {
			calls++
			if calls > 1 {
				return context.Canceled
			}
			return nil
		}
		_, err = runSQLDriverQuery(context.Background(), conn, "mysql", q, "read", check)
		var queryErr *database.QueryError
		want := "rolled_back"
		if rollbackFail {
			want = "unknown"
		}
		if !errors.As(err, &queryErr) || queryErr.Code != "database_query_authority_changed" || queryErr.Outcome != want {
			t.Fatalf("write-query revocation reported incorrect outcome: rollback failure=%v error=%+v", rollbackFail, queryErr)
		}
		conn.Close()
		db.Close()
	}
}

func TestSQLDriverStartedWriteRollbackFailuresAreUnknown(t *testing.T) {
	for _, execFail := range []bool{true, false} {
		for _, rollbackFail := range []bool{true, false} {
			db := sql.OpenDB(queryRollbackConnector{fail: rollbackFail, execFail: execFail})
			conn, err := db.Conn(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			write := false
			q := database.QueryRequest{SQL: "INSERT INTO fixture VALUES(1)", ReadOnly: &write}
			q.Validate()
			calls := 0
			check := func(context.Context) error {
				calls++
				if !execFail && calls > 1 {
					return context.Canceled
				}
				return nil
			}
			_, err = runSQLDriverQuery(context.Background(), conn, "mysql", q, "dml", check)
			var queryErr *database.QueryError
			want := "rolled_back"
			if rollbackFail {
				want = "unknown"
			}
			if !errors.As(err, &queryErr) || queryErr.Outcome != want {
				t.Fatalf("exec failure=%v rollback failure=%v: %v", execFail, rollbackFail, err)
			}
			conn.Close()
			db.Close()
		}
	}
}

func TestSQLDriverHonorsNontransactionalWrites(t *testing.T) {
	for _, kind := range []string{"dml", "read"} {
		for _, revoked := range []bool{false, true} {
			begins := 0
			db := sql.OpenDB(queryRollbackConnector{begins: &begins})
			conn, err := db.Conn(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			write := false
			q := database.QueryRequest{SQL: "INSERT INTO fixture VALUES(1)", ReadOnly: &write, ExecutionMode: "nontransactional"}
			if kind == "read" {
				q.SQL = "WITH mutation AS (INSERT INTO fixture VALUES(1) RETURNING value) SELECT value FROM mutation"
			}
			if err = q.Validate(); err != nil {
				t.Fatal(err)
			}
			checks := 0
			result, err := runSQLDriverQuery(context.Background(), conn, "mysql", q, kind, func(context.Context) error {
				checks++
				if revoked && checks == 2 {
					return context.Canceled
				}
				return nil
			})
			if begins != 0 {
				t.Fatal("nontransactional write opened transaction", kind, begins)
			}
			if revoked {
				var failure *database.QueryError
				if !errors.As(err, &failure) || failure.Code != "database_query_authority_changed" || failure.Outcome != "unknown" {
					t.Fatalf("kind=%s error=%v", kind, err)
				}
			} else if err != nil || result.Outcome != "applied" {
				t.Fatalf("kind=%s result=%+v error=%v", kind, result, err)
			}
			conn.Close()
			db.Close()
		}
	}
}
func TestSQLDriverReadOnlyModeStillUsesTransaction(t *testing.T) {
	begins := 0
	db := sql.OpenDB(queryRollbackConnector{begins: &begins})
	defer db.Close()
	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	q := database.QueryRequest{SQL: "SELECT 1", ExecutionMode: "nontransactional"}
	if err = q.Validate(); err != nil {
		t.Fatal(err)
	}
	result, err := runSQLDriverQuery(context.Background(), conn, "oracle", q, "read", func(context.Context) error { return nil })
	if err != nil || result.Outcome != "read" || begins != 1 {
		t.Fatalf("read-only transaction begins=%d error=%v", begins, err)
	}
}
