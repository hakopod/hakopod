package api

import (
	"encoding/json"
	"io"
	"net/http"
	"regexp"

	"github.com/hakopod/hakopod/internal/invocation"
)

func (s *Server) registerInvocationRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/applications/{id}/services/{service}/invocations", s.createInvocation)
	mux.HandleFunc("GET /api/v1/applications/{id}/services/{service}/invocations", s.listInvocations)
	mux.HandleFunc("GET /api/v1/applications/{id}/services/{service}/invocations/{invocation}", s.getInvocation)
	mux.HandleFunc("POST /api/v1/applications/{id}/services/{service}/invocations/{invocation}/cancel", s.cancelInvocation)
	mux.HandleFunc("GET /api/v1/applications/{id}/services/{service}/invocations/{invocation}/logs", s.invocationLogs)
}

// The durable receipt records the outcome before cleanup. Public status remains
// running until the worker confirms that the job and owned pods are absent.
func publicInvocation(v invocation.Record) invocation.Record {
	if v.CleanupPending {
		v.Status = invocation.Running
		v.FinishedAt = nil
		v.Message = "Invocation finished; runtime cleanup is pending"
	}
	return v
}
func invocationOwner(w http.ResponseWriter, r *http.Request) (string, bool) {
	w.Header().Set("Cache-Control", "no-store")
	owner, err := invocation.OwnerHash(r.Header.Get("X-Hakopod-Owner-Scope"))
	if err != nil {
		problem(w, 400, "invalid_owner", "X-Hakopod-Owner-Scope must contain a valid owner identifier")
		return "", false
	}
	return owner, true
}
func (s *Server) createInvocation(w http.ResponseWriter, r *http.Request) {
	owner, ok := invocationOwner(w, r)
	if !ok {
		return
	}
	if len(s.authEncryptionKey()) != 32 {
		problem(w, 503, "unavailable", "Job invocation encryption is not configured")
		return
	}
	var in invocation.CreateRequest
	r.Body = http.MaxBytesReader(w, r.Body, invocation.MaxInputBytes+8192)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&in); err != nil {
		problem(w, 400, "invalid_request", "Request must contain one valid invocation object")
		return
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		problem(w, 400, "invalid_request", "Request must contain one valid invocation object")
		return
	}
	bodyOwner, err := invocation.OwnerHash(in.OwnerScope)
	if err != nil || owner != bodyOwner {
		problem(w, 400, "invalid_owner", "Body owner_scope must match X-Hakopod-Owner-Scope")
		return
	}
	if in.ExpectedRevision < 1 || len(in.ExpectedImage) > 512 || !regexp.MustCompile(`@sha256:[a-f0-9]{64}$`).MatchString(in.ExpectedImage) {
		problem(w, 400, "invalid_template", "expected_revision and an immutable expected_image are required")
		return
	}
	if err = invocation.ValidateCorrelationID(in.CorrelationID); err != nil {
		problem(w, 400, "invalid_correlation", "correlation_id must be a valid identifier with at most 160 characters")
		return
	}
	v, err := s.Store.EnqueueInvocation(r.Context(), who(r), r.PathValue("id"), r.PathValue("service"), r.Header.Get("Idempotency-Key"), in, s.authEncryptionKey())
	if err != nil {
		failure(w, err)
		return
	}
	write(w, 202, publicInvocation(v))
}
func (s *Server) listInvocations(w http.ResponseWriter, r *http.Request) {
	owner, ok := invocationOwner(w, r)
	if !ok {
		return
	}
	correlation := r.URL.Query().Get("correlation_id")
	if invocation.ValidateCorrelationID(correlation) != nil {
		problem(w, 400, "invalid_correlation", "An exact correlation_id filter is required")
		return
	}
	active := r.URL.Query().Get("active")
	if active != "" && active != "true" && active != "false" {
		problem(w, 400, "invalid_filter", "active must be true or false")
		return
	}
	items, err := s.Store.ListInvocations(r.Context(), who(r), r.PathValue("id"), r.PathValue("service"), owner, correlation, r.Header.Get("Idempotency-Key"), active == "true")
	if err != nil {
		failure(w, err)
		return
	}
	for i := range items {
		items[i] = publicInvocation(items[i])
	}
	write(w, 200, map[string]any{"items": items})
}
func (s *Server) getInvocation(w http.ResponseWriter, r *http.Request) {
	owner, ok := invocationOwner(w, r)
	if !ok {
		return
	}
	v, err := s.Store.ReadInvocation(r.Context(), who(r), r.PathValue("id"), r.PathValue("service"), owner, r.PathValue("invocation"), "jobs:read")
	if err != nil {
		failure(w, err)
		return
	}
	write(w, 200, publicInvocation(v))
}
func (s *Server) cancelInvocation(w http.ResponseWriter, r *http.Request) {
	owner, ok := invocationOwner(w, r)
	if !ok {
		return
	}
	v, err := s.Store.CancelInvocation(r.Context(), who(r), r.PathValue("id"), r.PathValue("service"), owner, r.PathValue("invocation"))
	if err != nil {
		failure(w, err)
		return
	}
	write(w, 202, publicInvocation(v))
}
func (s *Server) invocationLogs(w http.ResponseWriter, r *http.Request) {
	owner, ok := invocationOwner(w, r)
	if !ok {
		return
	}
	v, err := s.Store.ReadInvocation(r.Context(), who(r), r.PathValue("id"), r.PathValue("service"), owner, r.PathValue("invocation"), "jobs:logs")
	if err != nil {
		failure(w, err)
		return
	}
	if !v.Terminal() {
		problem(w, 409, "logs_pending", "Logs become available after the invocation finishes")
		return
	}
	var logs []byte
	if len(v.EncryptedLogs) > 0 {
		logs, err = invocation.Open(s.authEncryptionKey(), v.ID, "logs", v.EncryptedLogs)
		if err != nil {
			problem(w, 503, "logs_unavailable", "Invocation logs could not be decrypted")
			return
		}
	}
	write(w, 200, map[string]any{"text": string(logs), "truncated": v.LogTruncated})
}
