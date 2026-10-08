package cluster

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"reflect"
	"testing"
	"time"
)

type mysqlRollbackProbe struct {
	events   []string
	warnings int64
	fail     bool
	cancel   bool
}

func (p *mysqlRollbackProbe) Connect(context.Context) (driver.Conn, error) { return p, nil }
func (p *mysqlRollbackProbe) Driver() driver.Driver                        { return queryRollbackDriver{} }
func (p *mysqlRollbackProbe) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("diagnostics must not prepare")
}
func (p *mysqlRollbackProbe) Close() error              { return nil }
func (p *mysqlRollbackProbe) Begin() (driver.Tx, error) { return mysqlRollbackProbeTx{p}, nil }
func (p *mysqlRollbackProbe) ExecContext(ctx context.Context, q string, _ []driver.NamedValue) (driver.Result, error) {
	p.events = append(p.events, q)
	if q != "ROLLBACK" {
		return nil, errors.New("unexpected SQL")
	}
	if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > 2*time.Second {
		return nil, errors.New("unbounded rollback")
	}
	if p.cancel {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if p.fail {
		return nil, errors.New("rollback unavailable")
	}
	return driver.RowsAffected(0), nil
}
func (p *mysqlRollbackProbe) QueryContext(_ context.Context, q string, _ []driver.NamedValue) (driver.Rows, error) {
	p.events = append(p.events, q)
	if q != "SHOW COUNT(*) WARNINGS" {
		return nil, errors.New("unexpected diagnostic")
	}
	return &mysqlRollbackCountRows{count: p.warnings}, nil
}

type mysqlRollbackProbeTx struct{ p *mysqlRollbackProbe }

func (t mysqlRollbackProbeTx) Commit() error { return errors.New("unexpected commit") }
func (t mysqlRollbackProbeTx) Rollback() error {
	t.p.events = append(t.p.events, "release_tx")
	return nil
}

type mysqlRollbackCountRows struct {
	count int64
	done  bool
}

func (*mysqlRollbackCountRows) Columns() []string { return []string{"warnings"} }
func (*mysqlRollbackCountRows) Close() error      { return nil }
func (r *mysqlRollbackCountRows) Next(v []driver.Value) error {
	if r.done {
		return io.EOF
	}
	r.done = true
	v[0] = r.count
	return nil
}
func TestMySQLRollbackDiagnosticsOrdering(t *testing.T) {
	for _, warnings := range []int64{0, 1, 1196} {
		p := &mysqlRollbackProbe{warnings: warnings}
		db := sql.OpenDB(p)
		tx, err := db.BeginTx(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		got := sqlDriverRollbackOutcome(context.Background(), tx, "mysql")
		want := "unknown"
		if warnings == 0 {
			want = "rolled_back"
		}
		if got != want || !reflect.DeepEqual(p.events, []string{"ROLLBACK", "SHOW COUNT(*) WARNINGS", "release_tx"}) {
			t.Fatalf("outcome=%s events=%v", got, p.events)
		}
		db.Close()
	}
}
func TestMySQLRollbackDiagnosticsPreserveCancellation(t *testing.T) {
	p := &mysqlRollbackProbe{cancel: true}
	db := sql.OpenDB(p)
	defer db.Close()
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	started := time.Now()
	if sqlDriverRollbackOutcome(ctx, tx, "mysql") != "unknown" || time.Since(started) > time.Second {
		t.Fatal("cancelled rollback was not conservative and bounded")
	}
	for _, event := range p.events {
		if event == "SHOW COUNT(*) WARNINGS" {
			t.Fatal("diagnostic continued after cancellation")
		}
	}
}
