package cluster

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/hakopod/hakopod/internal/database"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// This classification selects execution semantics. It is not the authorization
// boundary. The driver prepares one statement and the server enforces read-only
// mode. Engine qualification must test functions and implicit commits.
func queryStatementKind(statement string) (string, error) {
	fields := strings.Fields(statement)
	if len(fields) == 0 {
		return "", fmt.Errorf("a SQL statement is required")
	}
	kind := strings.ToUpper(fields[0])
	switch kind {
	case "SELECT", "WITH", "SHOW", "DESCRIBE", "EXPLAIN":
		return "read", nil
	case "INSERT", "UPDATE", "DELETE", "REPLACE", "MERGE":
		return "dml", nil
	case "CREATE", "ALTER", "DROP", "TRUNCATE", "RENAME":
		return "ddl", nil
	}
	return "", &database.QueryError{Code: "database_query_statement_unsupported", Outcome: "not_started"}
}
func (c *Client) querySQLDriver(ctx context.Context, d database.Resource, q database.QueryRequest, check func(context.Context) error) (database.QueryResult, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	kind, err := queryStatementKind(q.SQL)
	if err != nil {
		return database.QueryResult{}, err
	}
	if q.IsReadOnly() && kind != "read" {
		return database.QueryResult{}, &database.QueryError{Code: "database_query_read_only", Outcome: "not_started"}
	}
	observation, err := c.ObserveDatabase(ctx, d)
	if err != nil || observation.Status != "ready" {
		return database.QueryResult{}, queryUnavailable()
	}
	var member database.Member
	for _, m := range observation.Members {
		if m.Name == observation.Primary || len(observation.Members) == 1 {
			member = m
			break
		}
	}
	if member.UID == "" {
		return database.QueryResult{}, queryUnavailable()
	}
	var client *sql.DB
	switch d.Spec.Engine {
	case "oracle":
		client, err = c.oracleApplicationConnectionOptions(ctx, d, member, true, true)
	case "duckdb":
		// The managed MyDuck runtime currently exposes an owner credential. Do not
		// claim server-enforced read-only mode before the runtime proves support.
		password, identity, e := c.myduckClientIdentity(ctx, d)
		err = e
		if err == nil {
			client, err = c.myduckMySQLClient(ctx, d, member, password, identity)
		}
	case "vitess":
		if observation.Routing == nil || len(observation.Routing.Members) == 0 {
			return database.QueryResult{}, queryUnavailable()
		}
		member = observation.Routing.Members[0]
		secret, e := c.kube.CoreV1().Secrets(DatabaseNamespace(d.ID)).Get(ctx, "database-credentials", metav1.GetOptions{})
		err = e
		if err == nil {
			ns, e := c.kube.CoreV1().Namespaces().Get(ctx, DatabaseNamespace(d.ID), metav1.GetOptions{})
			if e != nil || !postgresQueryCredentialOwned(secret, d, ns) {
				return database.QueryResult{}, queryUnavailable()
			}
			trust, e := c.DatabaseTrust(ctx, d)
			err = e
			if e == nil {
				identity, e := redisTLSConfig(trust, "database."+DatabaseNamespace(d.ID)+".svc")
				err = e
				if e == nil {
					client, err = c.vitessGatewayClientWithReadTimeout(ctx, d, member, "app@primary", secret.Data["password"], identity, 20*time.Second)
				}
			}
		}
	case "mysql":
		client, err = c.mysqlQueryClient(ctx, d, member)
	}
	if err != nil || client == nil {
		return database.QueryResult{}, queryUnavailable()
	}
	defer client.Close()
	connection, err := client.Conn(ctx)
	if err != nil {
		return database.QueryResult{}, queryUnavailable()
	}
	defer connection.Close()
	if d.Spec.Engine == "oracle" {
		if err = boundOracleQueryConnection(connection); err != nil {
			return database.QueryResult{}, queryUnavailable()
		}
	}
	secret, err := c.kube.CoreV1().Secrets(DatabaseNamespace(d.ID)).Get(ctx, "database-credentials", metav1.GetOptions{})
	if err != nil {
		return database.QueryResult{}, queryUnavailable()
	}
	verify := func(step context.Context) error {
		if err := check(step); err != nil {
			return err
		}
		current, e := c.kube.CoreV1().Secrets(DatabaseNamespace(d.ID)).Get(step, "database-credentials", metav1.GetOptions{})
		if e != nil || current.UID != secret.UID || current.ResourceVersion != secret.ResourceVersion {
			return queryUnavailable()
		}
		if d.Spec.Engine == "vitess" {
			_, _, e = c.vitessExecTarget(step, d, member)
		} else {
			_, _, e = c.databaseExecTarget(step, d, member)
		}
		return e
	}
	if err := verify(ctx); err != nil {
		return database.QueryResult{}, queryUnavailable()
	}
	return runSQLDriverQuery(ctx, connection, d.Spec.Engine, q, kind, verify)
}
func runSQLDriverQuery(ctx context.Context, conn *sql.Conn, engine string, q database.QueryRequest, kind string, check func(context.Context) error) (database.QueryResult, error) {
	result := database.QueryResult{ReadOnly: q.IsReadOnly(), Columns: []database.QueryColumn{}, Rows: [][]*string{}, Outcome: "read"}
	transactional := q.IsReadOnly() || kind != "ddl" && q.ExecutionMode != "nontransactional"
	if kind == "ddl" && q.ExecutionMode != "nontransactional" {
		return result, &database.QueryError{Code: "database_query_nontransactional_mode_required", Outcome: "not_started"}
	}
	var tx *sql.Tx
	var err error
	if transactional {
		if q.IsReadOnly() && engine != "oracle" {
			if _, err = conn.ExecContext(ctx, "SET SESSION TRANSACTION READ ONLY"); err != nil {
				return result, &database.QueryError{Code: "database_query_read_only_mode_unavailable", Outcome: "not_started"}
			}
		}
		driverReadOnly := q.IsReadOnly() && engine != "oracle"
		tx, err = conn.BeginTx(ctx, &sql.TxOptions{ReadOnly: driverReadOnly})
		if err != nil {
			return result, &database.QueryError{Code: "database_query_read_only_mode_unavailable", Outcome: "not_started"}
		}
		defer tx.Rollback()
		if engine == "oracle" && q.IsReadOnly() {
			if _, err = tx.ExecContext(ctx, "SET TRANSACTION READ ONLY"); err != nil {
				return result, &database.QueryError{Code: "database_query_read_only_mode_unavailable", Outcome: "not_started"}
			}
		}
	}
	prepare := conn.PrepareContext
	if tx != nil {
		prepare = tx.PrepareContext
	}
	stmt, err := prepare(ctx, q.SQL)
	if err != nil {
		if sqlQueryFailureCode(engine, err) == "database_query_result_limit" {
			return result, &database.QueryError{Code: "database_query_result_limit", Outcome: "not_started"}
		}
		return result, &database.QueryError{Code: "database_query_statement_unsupported", Outcome: "not_started"}
	}
	defer stmt.Close()
	if err = check(ctx); err != nil {
		return result, queryUnavailable()
	}
	args := make([]any, len(q.Parameters))
	for i, v := range q.Parameters {
		if number, ok := v.(json.Number); ok {
			args[i] = string(number)
		} else {
			args[i] = v
		}
	}
	if kind != "read" {
		execution, e := stmt.ExecContext(ctx, args...)
		if e != nil {
			outcome := sqlRollbackOutcome(tx)
			return result, &database.QueryError{Code: sqlQueryFailureCode(engine, e), Outcome: outcome}
		}
		result.RowsAffected, _ = execution.RowsAffected()
		if err = check(ctx); err != nil {
			outcome := sqlRollbackOutcome(tx)
			return result, &database.QueryError{Code: "database_query_authority_changed", Outcome: outcome}
		}
		if tx != nil {
			if err = tx.Commit(); err != nil {
				return result, &database.QueryError{Code: sqlQueryFailureCode(engine, err), Outcome: "unknown"}
			}
			result.Outcome = "committed"
		} else {
			result.Outcome = "applied"
		}
		return result, nil
	}
	rows, err := stmt.QueryContext(ctx, args...)
	if err != nil {
		outcome := sqlRollbackOutcome(tx)
		return result, &database.QueryError{Code: sqlQueryFailureCode(engine, err), Outcome: outcome}
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		return result, &database.QueryError{Code: sqlQueryFailureCode(engine, err), Outcome: sqlRollbackOutcome(tx)}
	}
	types, _ := rows.ColumnTypes()
	for i, name := range columns {
		typeName := ""
		if i < len(types) {
			typeName = types[i].DatabaseTypeName()
		}
		result.Columns = append(result.Columns, database.QueryColumn{Name: name, TypeName: typeName})
	}
	metadata, _ := json.Marshal(result)
	used := len(metadata) + 256
	if used > q.MaxBytes {
		return result, &database.QueryError{Code: "database_query_result_limit", Outcome: sqlRollbackOutcome(tx)}
	}
	for rows.Next() {
		if len(result.Rows) >= q.MaxRows {
			result.Truncated = true
			break
		}
		values := make([]sql.NullString, len(columns))
		targets := make([]any, len(columns))
		for i := range values {
			targets[i] = &values[i]
		}
		if err = rows.Scan(targets...); err != nil {
			return result, &database.QueryError{Code: sqlQueryFailureCode(engine, err), Outcome: "unknown"}
		}
		row := make([]*string, len(columns))
		for i, v := range values {
			if v.Valid {
				copy := v.String
				row[i] = &copy
			}
		}
		encoded, _ := json.Marshal(row)
		if used+len(encoded) > q.MaxBytes {
			result.Truncated = true
			break
		}
		used += len(encoded)
		result.Rows = append(result.Rows, row)
	}
	if err = rows.Err(); err != nil {
		return result, &database.QueryError{Code: sqlQueryFailureCode(engine, err), Outcome: "unknown"}
	}
	rows.Close()
	if err = check(ctx); err != nil {
		outcome := "read"
		if !q.IsReadOnly() {
			outcome = sqlRollbackOutcome(tx)
		}
		return result, &database.QueryError{Code: "database_query_authority_changed", Outcome: outcome}
	}
	if result.Truncated && !q.IsReadOnly() && !transactional {
		return result, &database.QueryError{Code: "database_query_result_limit", Outcome: "unknown"}
	}
	if result.Truncated && !q.IsReadOnly() {
		return result, &database.QueryError{Code: "database_query_result_limit", Outcome: sqlRollbackOutcome(tx)}
	}
	if !q.IsReadOnly() {
		if err = check(ctx); err != nil {
			return result, &database.QueryError{Code: "database_query_authority_changed", Outcome: sqlRollbackOutcome(tx)}
		}
		if tx != nil {
			if err = tx.Commit(); err != nil {
				return result, &database.QueryError{Code: sqlQueryFailureCode(engine, err), Outcome: "unknown"}
			}
			result.Outcome = "committed"
		} else {
			result.Outcome = "applied"
		}
	}
	return result, nil
}

func sqlRollbackOutcome(tx *sql.Tx) string {
	if tx == nil || tx.Rollback() != nil {
		return "unknown"
	}
	return "rolled_back"
}
