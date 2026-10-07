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
}

func (c queryRollbackConnector) Connect(context.Context) (driver.Conn, error) {
	return queryRollbackConn{fail: c.fail, execFail: c.execFail}, nil
}
func (c queryRollbackConnector) Driver() driver.Driver { return queryRollbackDriver{} }

type queryRollbackDriver struct{}

func (queryRollbackDriver) Open(string) (driver.Conn, error) {
	return nil, errors.New("connector required")
}

type queryRollbackConn struct {
	fail     bool
	execFail bool
}

func (c queryRollbackConn) Prepare(string) (driver.Stmt, error) {
	return queryRollbackStmt{fail: c.execFail}, nil
}
func (c queryRollbackConn) Close() error              { return nil }
func (c queryRollbackConn) Begin() (driver.Tx, error) { return queryRollbackTx{fail: c.fail}, nil }

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
func (queryRollbackStmt) Query([]driver.Value) (driver.Rows, error) { return nil, io.EOF }
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
