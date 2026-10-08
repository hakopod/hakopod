package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"
	"time"

	"github.com/hakopod/hakopod/internal/database"
	"github.com/hakopod/hakopod/internal/store"
)

func (s *Server) registerDatabaseQueryRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/databases/{id}/query-capabilities", s.databaseQueryCapabilities)
	mux.HandleFunc("POST /api/v1/databases/{id}/query", s.databaseQuery)
}
func (s *Server) databaseQueryCapabilities(w http.ResponseWriter, r *http.Request) {
	d, err := s.Store.Database(r.Context(), who(r), r.PathValue("id"), false)
	if err != nil {
		failure(w, err)
		return
	}
	write(w, http.StatusOK, database.CapabilitiesForQuery(d.Spec.Engine))
}
func databaseQueryAllowed(p store.Principal, d database.Resource, readOnly bool) bool {
	permissions := []string{"databases:query"}
	if !readOnly {
		permissions = append(permissions, "databases:write-query")
	}
	if p.Application != "" {
		return false
	}
	for _, permission := range permissions {
		if !p.Allows(permission, d.Project, d.Environment, "") {
			return false
		}
		if (p.CredentialType == "machine" || p.CredentialType == "cli") && !slices.Contains(p.Permissions, permission) {
			return false
		}
	}
	if p.CredentialType == "machine" {
		return p.Project == d.Project && p.Environment == d.Environment && p.Project != "" && p.Environment != ""
	}
	return p.CredentialType == "browser" || p.CredentialType == "cli"
}
func (s *Server) databaseQuery(w http.ResponseWriter, r *http.Request) {
	var q database.QueryRequest
	if !decodeDatabaseQuery(w, r, &q) {
		return
	}
	if err := q.Validate(); err != nil {
		problem(w, 400, "invalid_request", err.Error())
		return
	}
	d, err := s.Store.DatabaseInternal(r.Context(), r.PathValue("id"))
	if err != nil {
		failure(w, err)
		return
	}
	if !databaseQueryAllowed(who(r), d, q.IsReadOnly()) {
		problem(w, 404, "not_found", "resource not found")
		return
	}
	capabilities := database.CapabilitiesForQuery(d.Spec.Engine)
	if q.ExecutionMode == "" && len(capabilities.ExecutionModes) > 0 {
		q.ExecutionMode = capabilities.ExecutionModes[0]
	}
	if (!q.IsReadOnly() || q.ExpectedRevision != 0) && q.ExpectedRevision != d.Revision {
		problem(w, 409, "database_query_revision_conflict", "Supply the reviewed database revision as expected_revision. Read the current database before another review.")
		return
	}
	if d.Status != "ready" || (!q.IsReadOnly() && d.Recovery != nil && d.Recovery.InspectedAt == nil) {
		problem(w, 409, "database_query_unavailable", "The database is not ready for SQL queries.")
		return
	}
	if s.Cluster == nil {
		problem(w, 503, "database_query_unavailable", "The database query connection is unavailable.")
		return
	}
	operationID := store.NewID()
	sum := sha256.Sum256([]byte(q.SQL))
	audit := func(outcome string) error {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		metadata := map[string]any{"operation_id": operationID, "read_only": q.IsReadOnly(), "execution_mode": q.ExecutionMode, "statement_sha256": hex.EncodeToString(sum[:]), "outcome": outcome}
		_, e := s.Store.Pool.Exec(ctx, "INSERT INTO audit_events(identity_id,key_id,action,resource,metadata) VALUES($1,$2,'database.query',$3,$4)", who(r).ID, who(r).KeyID, d.ID, store.JSON(metadata))
		return e
	}
	if err = audit("started"); err != nil {
		failure(w, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	check := func(step context.Context) error {
		bounded, done := context.WithTimeout(step, time.Second)
		defer done()
		p, e := s.freshRuntimePrincipal(r.WithContext(bounded))
		if e != nil || !databaseQueryAllowed(p, d, q.IsReadOnly()) {
			return store.ErrForbidden
		}
		current, e := s.Store.DatabaseInternal(bounded, d.ID)
		if e != nil || current.Revision != d.Revision || current.Status != "ready" || current.Project != d.Project || current.Environment != d.Environment {
			return store.ErrForbidden
		}
		return nil
	}
	if err = check(ctx); err != nil {
		_ = audit("not_started")
		failure(w, err)
		return
	}
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
				if check(ctx) != nil {
					cancel()
					return
				}
			}
		}
	}()
	defer func() { cancel(); <-guardDone }()
	result, err := s.Cluster.QueryDatabase(ctx, d, q, check)
	outcome := result.Outcome
	if err != nil {
		queryErr := &database.QueryError{Code: "database_query_failed", Outcome: "not_started"}
		_ = errors.As(err, &queryErr)
		if audit(queryErr.Outcome) != nil {
			queryErr = &database.QueryError{Code: "database_query_audit_failed", Outcome: queryErr.Outcome}
		}
		writeDatabaseQueryError(w, operationID, queryErr)
		return
	}
	result.OperationID = operationID
	result.DatabaseID = d.ID
	if err = audit(outcome); err != nil {
		writeDatabaseQueryError(w, operationID, &database.QueryError{Code: "database_query_audit_failed", Outcome: outcome})
		return
	}
	write(w, 200, result)
}
func writeDatabaseQueryError(w http.ResponseWriter, id string, e *database.QueryError) {
	status := 422
	message := "The database rejected the query."
	switch e.Code {
	case "database_query_engine_unsupported":
		status = 422
		message = "SQL queries are unavailable for this database engine. Read its query capabilities before execution."
	case "database_query_statement_unsupported":
		message = "This SQL statement is unavailable through the query API. Submit one statement supported by this engine."
	case "database_query_statement_syntax_unsupported":
		message = "This statement syntax is unsupported. Use one statement with standard quoted strings and identifiers. Put other values in bound parameters."
	case "database_query_nontransactional_mode_required":
		message = "This statement cannot run in a transaction. Review the statement with execution_mode set to nontransactional."
	case "database_query_read_only_mode_unavailable":
		message = "The database cannot enforce read-only execution for this request."
	case "database_query_parameter_unsupported":
		message = "A parameter has an unsupported type. Read the engine's parameter format before execution."
	case "database_query_execution_mode_unsupported":
		message = "This execution mode is unavailable. Read the database query capabilities before execution."
	case "database_query_read_only":
		status = 403
		message = "The read-only transaction rejected the write."
	case "database_query_tls_required":
		status = 409
		message = "Verified database TLS is required."
	case "database_query_busy":
		status = 429
		message = "All query slots are busy."
	case "database_query_unavailable":
		status = 503
		message = "The database query connection is unavailable."
	case "database_query_result_limit":
		message = "The result exceeded the query limit. Check the reported outcome before retrying."
	case "database_query_audit_failed":
		status = 503
		message = "The query audit could not be saved. Check the reported outcome before retrying."
	}
	if e.Outcome == "unknown" {
		status = 503
		message = "The execution outcome is unknown. Check the database before retrying."
	}
	write(w, status, map[string]any{"error": map[string]string{"code": e.Code, "message": message}, "operation_id": id, "outcome": e.Outcome})
}

func decodeDatabaseQuery(w http.ResponseWriter, r *http.Request, q *database.QueryRequest) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 128<<10)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	decoder.UseNumber()
	if err := decoder.Decode(q); err != nil {
		problem(w, 400, "invalid_request", "Submit one JSON query object with known fields.")
		return false
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		problem(w, 400, "invalid_request", "Submit one JSON query object.")
		return false
	}
	return true
}
