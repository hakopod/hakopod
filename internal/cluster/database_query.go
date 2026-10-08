package cluster

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"strconv"
	"time"

	"github.com/hakopod/hakopod/internal/database"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// Bound SQL execution across all API servers in this process.
var databaseQuerySlots = make(chan struct{}, 4)

func (c *Client) QueryDatabase(ctx context.Context, d database.Resource, q database.QueryRequest, checks ...func(context.Context) error) (database.QueryResult, error) {
	empty := database.QueryResult{}
	if err := q.Validate(); err != nil {
		return empty, err
	}
	if !database.CapabilitiesForQuerySpec(d.Spec).Supported {
		return empty, &database.QueryError{Code: "database_query_engine_unsupported", Outcome: "not_started"}
	}
	capabilities := database.CapabilitiesForQuerySpec(d.Spec)
	if q.IsReadOnly() && !capabilities.ReadOnlySupported {
		return empty, &database.QueryError{Code: "database_query_read_only_unsupported", Outcome: "not_started"}
	}
	mode := q.ExecutionMode
	if mode == "" && len(capabilities.ExecutionModes) > 0 {
		mode = capabilities.ExecutionModes[0]
	}
	allowedMode := false
	for _, supportedMode := range capabilities.ExecutionModes {
		if mode == supportedMode {
			allowedMode = true
		}
	}
	if !allowedMode {
		return empty, &database.QueryError{Code: "database_query_execution_mode_unsupported", Outcome: "not_started"}
	}
	q.ExecutionMode = mode
	if !d.Spec.TLSRequired() {
		return empty, &database.QueryError{Code: "database_query_tls_required", Outcome: "not_started"}
	}
	if c == nil || c.kube == nil || c.dynamic == nil {
		return empty, queryUnavailable()
	}
	select {
	case databaseQuerySlots <- struct{}{}:
		defer func() { <-databaseQuerySlots }()
	default:
		return empty, &database.QueryError{Code: "database_query_busy", Outcome: "not_started"}
	}
	check := func(step context.Context) error {
		for _, guard := range checks {
			if guard != nil {
				if err := guard(step); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := check(ctx); err != nil {
		return empty, queryUnavailable()
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	guardDone := make(chan struct{})
	go func() {
		defer close(guardDone)
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				step, done := context.WithTimeout(ctx, time.Second)
				err := check(step)
				done()
				if err != nil {
					cancel()
					return
				}
			}
		}
	}()
	defer func() { cancel(); <-guardDone }()

	if d.Spec.Engine != "postgresql" {
		return c.querySQLEngine(ctx, d, q, check)
	}
	ns, err := c.kube.CoreV1().Namespaces().Get(ctx, DatabaseNamespace(d.ID), metav1.GetOptions{})
	if err != nil || ns.UID == "" || ns.DeletionTimestamp != nil {
		return empty, queryUnavailable()
	}
	for k, v := range databaseLabels(d) {
		if ns.Labels[k] != v {
			return empty, queryUnavailable()
		}
	}
	object, err := c.dynamic.Resource(pgDatabaseResource).Namespace(ns.Name).Get(ctx, "database", metav1.GetOptions{})
	if err != nil || object.GetUID() == "" || object.GetDeletionTimestamp() != nil {
		return empty, queryUnavailable()
	}
	for k, v := range databaseLabels(d) {
		if object.GetLabels()[k] != v {
			return empty, queryUnavailable()
		}
	}
	primary, _, _ := unstructured.NestedString(object.Object, "status", "currentPrimary")
	if primary == "" {
		return empty, queryUnavailable()
	}
	current, err := c.kube.CoreV1().Pods(ns.Name).Get(ctx, primary, metav1.GetOptions{})
	if err != nil || current.UID == "" {
		return empty, queryUnavailable()
	}
	ready := false
	for _, cond := range current.Status.Conditions {
		if cond.Type == corev1.PodReady && cond.Status == corev1.ConditionTrue {
			ready = true
		}
	}
	if !ready {
		return empty, queryUnavailable()
	}
	pod, _, err := c.databaseExecTarget(ctx, d, database.Member{Name: primary, UID: string(current.UID)})
	if err != nil {
		return empty, queryUnavailable()
	}
	secret, err := c.kube.CoreV1().Secrets(ns.Name).Get(ctx, "database-credentials", metav1.GetOptions{})
	if err != nil || !postgresQueryCredentialOwned(secret, d, ns) {
		return empty, queryUnavailable()
	}
	trust, err := c.DatabaseTrust(ctx, d)
	if err != nil {
		return empty, queryUnavailable()
	}
	host := "database-rw." + ns.Name + ".svc"
	identity, err := redisTLSConfig(trust, host)
	if err != nil {
		return empty, queryUnavailable()
	}
	config, err := pgx.ParseConfig("host=" + host + " port=5432 user=app dbname=app connect_timeout=5")
	if err != nil {
		return empty, queryUnavailable()
	}
	config.Password = string(secret.Data["password"])
	config.TLSConfig = identity
	config.Fallbacks = nil
	config.DefaultQueryExecMode = pgx.QueryExecModeExec
	config.MaxProtocolMessageBodyLen = database.QueryMaxBytes
	config.OnNotice = nil
	config.RuntimeParams["statement_timeout"] = "15000"
	config.RuntimeParams["lock_timeout"] = "3000"
	config.RuntimeParams["idle_in_transaction_session_timeout"] = "15000"
	config.LookupFunc = func(context.Context, string) ([]string, error) { return []string{"127.0.0.1"}, nil }
	config.DialFunc = func(step context.Context, network, address string) (net.Conn, error) {
		if network != "tcp" || step.Err() != nil {
			return nil, queryUnavailable()
		}
		return c.postgresQueryStream(ctx, d, database.Member{Name: primary, UID: string(pod.UID)})
	}
	conn, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		return empty, queryUnavailable()
	}
	defer closeQueryConnection(conn)
	// Reject elevated app grants. The query connection never uses a root role.
	var unsafe bool
	err = conn.QueryRow(ctx, `SELECT current_user <> 'app' OR pg_is_in_recovery() OR EXISTS(SELECT 1 FROM pg_roles WHERE pg_has_role(current_user,oid,'MEMBER') AND (rolsuper OR rolcreaterole OR rolcreatedb OR rolreplication OR rolbypassrls OR rolname IN ('pg_read_server_files','pg_write_server_files','pg_execute_server_program')))`).Scan(&unsafe)
	if err != nil || unsafe {
		return empty, queryUnavailable()
	}
	verify := func(step context.Context) error {
		if err := check(step); err != nil {
			return err
		}
		current, e := c.kube.CoreV1().Secrets(ns.Name).Get(step, "database-credentials", metav1.GetOptions{})
		if e != nil || current.UID != secret.UID || current.ResourceVersion != secret.ResourceVersion || !postgresQueryCredentialOwned(current, d, ns) {
			return queryUnavailable()
		}
		_, _, e = c.databaseExecTarget(step, d, database.Member{Name: primary, UID: string(pod.UID)})
		return e
	}
	if err := verify(ctx); err != nil {
		return empty, queryUnavailable()
	}
	return runPostgresQuery(ctx, conn, q, verify)
}
func closeQueryConnection(conn *pgx.Conn) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_ = conn.Close(ctx)
}
func queryUnavailable() *database.QueryError {
	return &database.QueryError{Code: "database_query_unavailable", Outcome: "not_started"}
}
func postgresQueryCredentialOwned(s *corev1.Secret, d database.Resource, ns *corev1.Namespace) bool {
	if s == nil || ns == nil || ns.UID == "" || ns.Name != DatabaseNamespace(d.ID) || ns.DeletionTimestamp != nil || s.Namespace != ns.Name || s.Name != "database-credentials" || s.DeletionTimestamp != nil || s.Type != corev1.SecretTypeBasicAuth || s.Immutable == nil || !*s.Immutable || len(s.Data) != 2 || string(s.Data["username"]) != "app" || len(s.Data["password"]) != 64 {
		return false
	}
	for k, v := range databaseLabels(d) {
		if ns.Labels[k] != v || s.Labels[k] != v {
			return false
		}
	}
	return true
}
func queryParameters(values []any) [][]byte {
	out := make([][]byte, len(values))
	for i, v := range values {
		switch x := v.(type) {
		case string:
			out[i] = []byte(x)
		case bool:
			out[i] = []byte(strconv.FormatBool(x))
		case float64:
			out[i] = []byte(strconv.FormatFloat(x, 'g', -1, 64))
		case json.Number:
			out[i] = []byte(x.String())
		}
	}
	return out
}
func runPostgresQuery(ctx context.Context, conn *pgx.Conn, q database.QueryRequest, check func(context.Context) error) (database.QueryResult, error) {
	result := database.QueryResult{ReadOnly: q.IsReadOnly(), Columns: []database.QueryColumn{}, Rows: [][]*string{}, Outcome: "read"}
	access := pgx.ReadOnly
	if !result.ReadOnly {
		access = pgx.ReadWrite
	}
	tx, err := conn.BeginTx(ctx, pgx.TxOptions{AccessMode: access})
	if err != nil {
		return result, queryUnavailable()
	}
	defer func() {
		rollbackCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = tx.Rollback(rollbackCtx)
	}()
	// PostgreSQL parses one plannable statement. It rejects transaction control
	// and COPY commands. This prevents user SQL from ending the transaction.
	params := queryParameters(q.Parameters)
	explain := conn.PgConn().ExecParams(ctx, "EXPLAIN (FORMAT JSON) "+q.SQL, params, nil, []int16{0}, []int16{0})
	_, err = explain.Close()
	if err != nil {
		return result, &database.QueryError{Code: "database_query_statement_unsupported", Outcome: pgRollbackOutcome(tx)}
	}
	if check != nil {
		if err = check(ctx); err != nil {
			return result, &database.QueryError{Code: "database_query_authority_changed", Outcome: pgRollbackOutcome(tx)}
		}
	}
	var readOnly string
	if err = tx.QueryRow(ctx, "SELECT current_setting('transaction_read_only')").Scan(&readOnly); err != nil || (result.ReadOnly && readOnly != "on") {
		return result, queryUnavailable()
	}
	rr := conn.PgConn().ExecParams(ctx, q.SQL, params, nil, []int16{0}, []int16{0})
	for _, col := range rr.FieldDescriptions() {
		result.Columns = append(result.Columns, database.QueryColumn{Name: col.Name, TypeOID: col.DataTypeOID})
	}
	encoded, _ := json.Marshal(result)
	used := len(encoded) + 256
	if used > q.MaxBytes {
		closeQueryConnection(conn)
		return result, &database.QueryError{Code: "database_query_result_limit", Outcome: pgRollbackOutcome(tx)}
	}
	for rr.NextRow() {
		if len(result.Rows) >= q.MaxRows {
			return limitedQueryResult(conn, result)
		}
		values := rr.Values()
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
			return result, &database.QueryError{Code: "database_query_failed", Outcome: pgRollbackOutcome(tx)}
		}
		if used+len(encoded)+1 > q.MaxBytes {
			return limitedQueryResult(conn, result)
		}
		used += len(encoded) + 1
		result.Rows = append(result.Rows, row)
	}
	tag, err := rr.Close()
	if err != nil {
		failure := postgresQueryFailure(err, false)
		failure.Outcome = pgRollbackOutcome(tx)
		return result, failure
	}
	result.RowsAffected = tag.RowsAffected()
	if result.ReadOnly {
		return result, nil
	}
	if check != nil {
		if err = check(ctx); err != nil {
			return result, &database.QueryError{Code: "database_query_authority_changed", Outcome: pgRollbackOutcome(tx)}
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return result, postgresQueryFailure(err, true)
	}
	result.Outcome = "committed"
	return result, nil
}
func limitedQueryResult(conn *pgx.Conn, result database.QueryResult) (database.QueryResult, error) {
	closeQueryConnection(conn)
	if !result.ReadOnly {
		return result, &database.QueryError{Code: "database_query_result_limit", Outcome: "unknown"}
	}
	result.Truncated = true
	return result, nil
}
func postgresQueryFailure(err error, commit bool) *database.QueryError {
	var backend *pgconn.PgError
	if errors.As(err, &backend) {
		code := "database_query_failed"
		if backend.Code == "25006" {
			code = "database_query_read_only"
		}
		return &database.QueryError{Code: code, Outcome: "unknown"}
	}
	return &database.QueryError{Code: "database_query_failed", Outcome: "unknown"}
}

func pgRollbackOutcome(tx pgx.Tx) string {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if tx == nil || tx.Rollback(ctx) != nil {
		return "unknown"
	}
	return "rolled_back"
}
