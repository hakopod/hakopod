package cluster

import (
	"context"
	"encoding/json"
	"github.com/hakopod/hakopod/internal/database"
	"github.com/jackc/pgx/v5"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"time"
)

func (c *Client) queryMyDuckSQL(ctx context.Context, d database.Resource, q database.QueryRequest, check func(context.Context) error) (database.QueryResult, error) {
	result := database.QueryResult{ReadOnly: false, Columns: []database.QueryColumn{}, Rows: [][]*string{}, Outcome: "not_started"}
	if q.IsReadOnly() {
		return result, &database.QueryError{Code: "database_query_read_only_unsupported", Outcome: "not_started"}
	}
	if q.ExecutionMode != "nontransactional" {
		return result, &database.QueryError{Code: "database_query_nontransactional_mode_required", Outcome: "not_started"}
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	observation, err := c.ObserveDatabase(ctx, d)
	if err != nil || observation.Status != "ready" || len(observation.Members) != 1 {
		return result, queryUnavailable()
	}
	member := observation.Members[0]
	credential, identity, err := c.myduckClientIdentity(ctx, d)
	if err != nil {
		return result, queryUnavailable()
	}
	secret, err := c.kube.CoreV1().Secrets(DatabaseNamespace(d.ID)).Get(ctx, "database-credentials", metav1.GetOptions{})
	if err != nil {
		return result, queryUnavailable()
	}
	conn, err := c.myduckPostgresClientBounded(ctx, d, member, credential, identity, true)
	if err != nil {
		return result, queryUnavailable()
	}
	defer closeQueryConnection(conn)
	guard := check
	verify := func(step context.Context) error {
		if guard != nil {
			if err := guard(step); err != nil {
				return err
			}
		}
		current, e := c.kube.CoreV1().Secrets(DatabaseNamespace(d.ID)).Get(step, "database-credentials", metav1.GetOptions{})
		if e != nil || current.UID != secret.UID || current.ResourceVersion != secret.ResourceVersion {
			return queryUnavailable()
		}
		_, _, e = c.databaseExecTarget(step, d, member)
		return e
	}
	args := []any{pgx.QueryResultFormats{0}}
	for _, value := range q.Parameters {
		if number, ok := value.(json.Number); ok {
			args = append(args, string(number))
		} else {
			args = append(args, value)
		}
	}
	if err = verify(ctx); err != nil {
		return result, queryUnavailable()
	}
	rows, err := conn.Query(ctx, q.SQL, args...)
	if err != nil {
		return result, &database.QueryError{Code: "database_query_failed", Outcome: "unknown"}
	}
	defer rows.Close()
	for _, col := range rows.FieldDescriptions() {
		result.Columns = append(result.Columns, database.QueryColumn{Name: col.Name, TypeOID: col.DataTypeOID})
	}
	encoded, _ := json.Marshal(result)
	used := len(encoded) + 256
	if used > q.MaxBytes {
		closeQueryConnection(conn)
		return result, &database.QueryError{Code: "database_query_result_limit", Outcome: "unknown"}
	}
	for rows.Next() {
		if len(result.Rows) >= q.MaxRows {
			return myduckLimitedQueryResult(conn, result)
		}
		values := rows.RawValues()
		row := make([]*string, len(values))
		for i, v := range values {
			if v != nil {
				s := string(v)
				row[i] = &s
			}
		}
		encoded, err = json.Marshal(row)
		if err != nil {
			closeQueryConnection(conn)
			return result, &database.QueryError{Code: "database_query_failed", Outcome: "unknown"}
		}
		if used+len(encoded)+1 > q.MaxBytes {
			return myduckLimitedQueryResult(conn, result)
		}
		used += len(encoded) + 1
		result.Rows = append(result.Rows, row)
	}
	rows.Close()
	tag := rows.CommandTag()
	err = rows.Err()
	if err != nil {
		return result, &database.QueryError{Code: "database_query_failed", Outcome: "unknown"}
	}
	result.RowsAffected = tag.RowsAffected()
	if err = verify(ctx); err != nil {
		return result, &database.QueryError{Code: "database_query_authority_changed", Outcome: "unknown"}
	}
	result.Outcome = "applied"
	return result, nil
}

func myduckLimitedQueryResult(conn *pgx.Conn, result database.QueryResult) (database.QueryResult, error) {
	closeQueryConnection(conn)
	return result, &database.QueryError{Code: "database_query_result_limit", Outcome: "unknown"}
}
