package cluster

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/hakopod/hakopod/internal/database"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

func TestPostgresQueryCredentialScope(t *testing.T) {
	d := database.Resource{ID: strings.Repeat("a", 32), Project: "project", Environment: "development"}
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: DatabaseNamespace(d.ID), UID: types.UID("namespace"), Labels: databaseLabels(d)}}
	immutable := true
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "database-credentials", Namespace: ns.Name, Labels: databaseLabels(d)}, Type: corev1.SecretTypeBasicAuth, Immutable: &immutable, Data: map[string][]byte{"username": []byte("app"), "password": []byte(strings.Repeat("p", 64))}}
	if !postgresQueryCredentialOwned(secret, d, ns) {
		t.Fatal("valid app credentials rejected")
	}
	for _, mutation := range []func(*corev1.Secret){
		func(s *corev1.Secret) { s.Data["username"] = []byte("postgres") },
		func(s *corev1.Secret) { s.Labels["hakopod.io/project"] = "foreign" },
		func(s *corev1.Secret) { s.Namespace = "foreign" },
		func(s *corev1.Secret) { s.Data["password"] = []byte("short") },
		func(s *corev1.Secret) { s.Immutable = nil },
		func(s *corev1.Secret) { s.DeletionTimestamp = &metav1.Time{} },
	} {
		copy := secret.DeepCopy()
		mutation(copy)
		if postgresQueryCredentialOwned(copy, d, ns) {
			t.Fatal("changed credential identity accepted")
		}
	}
}
func TestPostgresQueryFailureOutcome(t *testing.T) {
	tests := []struct {
		err           error
		commit        bool
		code, outcome string
	}{
		{errors.New("connection lost SQL secret"), false, "database_query_failed", "unknown"},
		{errors.New("connection lost SQL secret"), true, "database_query_failed", "unknown"},
		{&pgconn.PgError{Code: "25006", Message: "secret statement"}, false, "database_query_read_only", "unknown"},
		{&pgconn.PgError{Code: "40001", Message: "secret statement"}, true, "database_query_failed", "unknown"},
	}
	for _, tt := range tests {
		got := postgresQueryFailure(tt.err, tt.commit)
		if got.Code != tt.code || got.Outcome != tt.outcome {
			t.Fatal(got)
		}
		if strings.Contains(got.Error(), "secret") {
			t.Fatal("backend data leaked")
		}
	}
}
func TestDatabaseQueryUnsupportedFailsBeforeClusterAccess(t *testing.T) {
	var c *Client
	for _, engine := range []string{"vitess", "oracle", "mongodb", "redis"} {
		_, err := c.QueryDatabase(context.Background(), database.Resource{Spec: database.Spec{Engine: engine}}, database.QueryRequest{SQL: "SELECT 1"})
		var queryErr *database.QueryError
		if !errors.As(err, &queryErr) || queryErr.Code != "database_query_engine_unsupported" || queryErr.Outcome != "not_started" {
			t.Fatalf("%s: %v", engine, err)
		}
	}
}
func TestSupportedDatabaseQueryRequiresTLSBeforeClusterAccess(t *testing.T) {
	var c *Client
	for _, engine := range []string{"postgresql", "mysql", "clickhouse"} {
		_, err := c.QueryDatabase(context.Background(), database.Resource{Spec: database.Spec{Engine: engine}}, database.QueryRequest{SQL: "SELECT 1"})
		var queryErr *database.QueryError
		if !errors.As(err, &queryErr) || queryErr.Code != "database_query_tls_required" || queryErr.Outcome != "not_started" {
			t.Fatalf("%s: %v", engine, err)
		}
	}
}

func TestDatabaseQueryParametersPreserveNullAndPrecision(t *testing.T) {
	input := []any{nil, "9007199254740993", true, false, 1.25}
	got := queryParameters(input)
	if got[0] != nil || string(got[1]) != "9007199254740993" || string(got[2]) != "true" || string(got[3]) != "false" || string(got[4]) != "1.25" {
		t.Fatal(got)
	}
}

// Native transaction acceptance uses a disposable table in a test PostgreSQL
// schema. The caller must provide a development database, never production.
func TestPostgresQueryTransaction(t *testing.T) {
	dsn := os.Getenv("HAKOPOD_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set HAKOPOD_TEST_DATABASE_URL for SQL transaction acceptance")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { closeQueryConnection(conn) }()
	table := fmt.Sprintf("query_fixture_%d", time.Now().UnixNano())
	falseValue := false
	_, err = conn.Exec(ctx, "CREATE TABLE "+table+" (value bigint)")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = conn.Exec(context.Background(), "DROP TABLE IF EXISTS "+table) }()
	read := database.QueryRequest{SQL: "INSERT INTO " + table + " VALUES (1)"}
	_ = read.Validate()
	_, err = runPostgresQuery(ctx, conn, read, nil)
	if err == nil {
		t.Fatal("read-only write succeeded")
	}
	write := database.QueryRequest{SQL: "INSERT INTO " + table + " VALUES ($1::bigint) RETURNING value", Parameters: []any{"9007199254740993"}, ReadOnly: &falseValue}
	_ = write.Validate()
	result, err := runPostgresQuery(ctx, conn, write, nil)
	if err != nil || result.Outcome != "committed" || len(result.Rows) != 1 || *result.Rows[0][0] != "9007199254740993" {
		t.Fatalf("write: %#v %v", result, err)
	}
	write.SQL = "INSERT INTO " + table + " SELECT generate_series(1,3) RETURNING value"
	write.Parameters = nil
	write.MaxRows = 1
	_, err = runPostgresQuery(ctx, conn, write, nil)
	var queryErr *database.QueryError
	if !errors.As(err, &queryErr) || queryErr.Outcome != "unknown" {
		t.Fatalf("closed connection claimed confirmed rollback: %v", err)
	}
	// The bounded result path closes the connection rather than draining rows.
	conn, err = pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	var count int
	if err = conn.QueryRow(ctx, "SELECT count(*) FROM "+table).Scan(&count); err != nil || count != 1 {
		t.Fatalf("limited write persisted: %d %v", count, err)
	}

	checks := 0
	revoked := database.QueryRequest{SQL: "INSERT INTO " + table + " VALUES(5)", ReadOnly: &falseValue}
	_ = revoked.Validate()
	_, err = runPostgresQuery(ctx, conn, revoked, func(context.Context) error {
		checks++
		if checks == 2 {
			return errors.New("grant revoked after execution")
		}
		return nil
	})
	if !errors.As(err, &queryErr) || queryErr.Code != "database_query_authority_changed" || queryErr.Outcome != "rolled_back" || checks != 2 {
		t.Fatalf("post-execution revocation: checks=%d error=%v", checks, err)
	}
	if err = conn.QueryRow(ctx, "SELECT count(*) FROM "+table).Scan(&count); err != nil || count != 1 {
		t.Fatalf("post-execution revoked write persisted: %d %v", count, err)
	}
	for _, sql := range []string{"COMMIT", "SET TRANSACTION READ WRITE", "SELECT 1; SELECT 2", "COPY " + table + " TO STDOUT"} {
		q := database.QueryRequest{SQL: sql}
		_ = q.Validate()
		_, err = runPostgresQuery(ctx, conn, q, nil)
		if !errors.As(err, &queryErr) || queryErr.Code != "database_query_statement_unsupported" {
			t.Fatalf("escape %q: %v", sql, err)
		}
	}
	q := database.QueryRequest{SQL: "INSERT INTO " + table + " VALUES(4)", ReadOnly: &falseValue}
	_ = q.Validate()
	_, err = runPostgresQuery(ctx, conn, q, func(context.Context) error { return errors.New("grant revoked") })
	if !errors.As(err, &queryErr) || queryErr.Code != "database_query_authority_changed" {
		t.Fatalf("revoked grant committed: %v", err)
	}
	if err = conn.QueryRow(ctx, "SELECT count(*) FROM "+table).Scan(&count); err != nil || count != 1 {
		t.Fatalf("revoked write persisted: %d %v", count, err)
	}
}

// Embed the unused transaction methods so the test isolates rollback acknowledgement.
type rollbackOutcomeTx struct {
	pgx.Tx
	err    error
	called bool
}

func (tx *rollbackOutcomeTx) Rollback(ctx context.Context) error {
	tx.called = true
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if _, bounded := ctx.Deadline(); !bounded {
		return errors.New("rollback has no deadline")
	}
	return tx.err
}
func TestPostgresRollbackRequiresAcknowledgement(t *testing.T) {
	for _, c := range []struct {
		name string
		err  error
		want string
	}{
		{"acknowledged", nil, "rolled_back"},
		{"connection lost", errors.New("connection lost"), "unknown"},
		{"cancelled", context.Canceled, "unknown"},
		{"transaction already closed", pgx.ErrTxClosed, "unknown"},
	} {
		t.Run(c.name, func(t *testing.T) {
			tx := &rollbackOutcomeTx{err: c.err}
			if got := pgRollbackOutcome(tx); got != c.want || !tx.called {
				t.Fatalf("outcome=%s called=%v", got, tx.called)
			}
		})
	}
	if got := pgRollbackOutcome(nil); got != "unknown" {
		t.Fatalf("missing transaction outcome=%s", got)
	}
}
