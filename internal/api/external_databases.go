package api

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"net/http"
	"sync"
	"time"

	"github.com/hakopod/hakopod/internal/externaldatabase"
	"github.com/hakopod/hakopod/internal/store"
)

func (s *Server) registerExternalDatabaseRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/external-databases", s.externalDatabases)
	mux.HandleFunc("POST /api/v1/external-databases", s.createExternalDatabase)
	mux.HandleFunc("GET /api/v1/external-databases/{id}", s.externalDatabase)
	mux.HandleFunc("PUT /api/v1/external-databases/{id}", s.updateExternalDatabase)
	mux.HandleFunc("DELETE /api/v1/external-databases/{id}", s.deleteExternalDatabase)
	mux.HandleFunc("GET /api/v1/external-databases/{id}/connections", s.externalDatabaseConnections)
	mux.HandleFunc("GET /api/v1/external-databases/{id}/trust", s.externalDatabaseTrust)
	mux.HandleFunc("POST /api/v1/external-databases/{id}/connection-plan", s.externalDatabaseConnectionPlan)
	mux.HandleFunc("POST /api/v1/external-databases/{id}/connect", s.externalDatabaseConnect)
	mux.HandleFunc("GET /api/v1/external-database-operations/{id}", s.externalDatabaseOperation)
}

func (s *Server) externalDatabases(w http.ResponseWriter, r *http.Request) {
	p, e := scope(r)
	if !validScope(p, e) {
		problem(w, 400, "invalid_request", "select a project and environment")
		return
	}
	items, err := s.Store.ExternalDatabases(r.Context(), who(r), p, e)
	if err != nil {
		failure(w, err)
		return
	}
	write(w, 200, map[string]any{"items": items})
}
func (s *Server) externalDatabase(w http.ResponseWriter, r *http.Request) {
	d, err := s.Store.ExternalDatabase(r.Context(), who(r), r.PathValue("id"), false)
	if err != nil {
		failure(w, err)
		return
	}
	write(w, 200, d)
}
func (s *Server) externalDatabaseOperation(w http.ResponseWriter, r *http.Request) {
	op, err := s.Store.ExternalDatabaseOperation(r.Context(), who(r), r.PathValue("id"))
	if err != nil {
		failure(w, err)
		return
	}
	write(w, 200, op)
}

func (s *Server) sealExternalDatabaseCredentials(d *externaldatabase.Resource, c externaldatabase.Credentials) error {
	key := s.authEncryptionKey()
	sealed, err := externaldatabase.SealCredentials(key, d.ID, d.Spec, c)
	if err != nil {
		return err
	}
	d.EncryptedCredentials = sealed
	digest := hmac.New(sha256.New, key)
	_, _ = digest.Write([]byte("external-database-credentials:" + d.ID))
	_, _ = digest.Write(store.JSON(c))
	d.CredentialDigest = digest.Sum(nil)
	return nil
}

func (s *Server) createExternalDatabase(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Project     string                       `json:"project"`
		Environment string                       `json:"environment"`
		Spec        externaldatabase.Spec        `json:"spec"`
		Credentials externaldatabase.Credentials `json:"credentials"`
	}
	if !decode(w, r, &in) {
		return
	}
	if !validScope(in.Project, in.Environment) {
		problem(w, 400, "invalid_request", "provide a valid project and environment")
		return
	}
	if !who(r).AllowsDatabase(in.Project, in.Environment, true) {
		failure(w, store.ErrForbidden)
		return
	}
	problem(w, 503, "unavailable", externaldatabase.ErrNewChangesUnavailable.Error())
}
func (s *Server) updateExternalDatabase(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Spec             externaldatabase.Spec         `json:"spec"`
		Credentials      *externaldatabase.Credentials `json:"credentials,omitempty"`
		ExpectedRevision int64                         `json:"expected_revision"`
		ConfirmName      string                        `json:"confirm_name"`
	}
	if !decode(w, r, &in) {
		return
	}
	d, err := s.Store.ExternalDatabaseInternal(r.Context(), r.PathValue("id"))
	if err != nil {
		failure(w, err)
		return
	}
	if !who(r).AllowsDatabase(d.Project, d.Environment, true) {
		failure(w, store.ErrForbidden)
		return
	}
	if in.Credentials == nil || in.Spec != d.Spec {
		problem(w, 503, "unavailable", "Only credential rotation is available for an existing PlanetScale connection; changing its configuration is unavailable")
		return
	}
	if in.ConfirmName != d.Spec.Name || in.ExpectedRevision < 1 {
		problem(w, 400, "invalid_request", "confirm the current connection name and revision")
		return
	}
	if err = in.Credentials.Validate(); err != nil {
		problem(w, 400, "invalid_request", err.Error())
		return
	}
	if err = s.sealExternalDatabaseCredentials(&d, *in.Credentials); err != nil {
		failure(w, err)
		return
	}
	idem, ok := backupIdempotency(w, r)
	if !ok {
		return
	}
	op, err := s.Store.AcceptExternalDatabase(r.Context(), who(r), d, in.ExpectedRevision, idem, "update", true)
	if err != nil {
		failure(w, err)
		return
	}
	write(w, 202, op)
}
func (s *Server) deleteExternalDatabase(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ExpectedRevision int64  `json:"expected_revision"`
		ConfirmName      string `json:"confirm_name"`
	}
	if !decode(w, r, &in) {
		return
	}
	d, err := s.Store.ExternalDatabaseInternal(r.Context(), r.PathValue("id"))
	if err != nil {
		failure(w, err)
		return
	}
	if !who(r).AllowsDatabase(d.Project, d.Environment, true) {
		failure(w, store.ErrForbidden)
		return
	}
	if in.ConfirmName != d.Spec.Name || in.ExpectedRevision < 1 {
		problem(w, 400, "invalid_request", "confirm the current connection name and revision")
		return
	}
	idem, ok := backupIdempotency(w, r)
	if !ok {
		return
	}
	op, err := s.Store.AcceptExternalDatabase(r.Context(), who(r), d, in.ExpectedRevision, idem, "delete", false)
	if err != nil {
		failure(w, err)
		return
	}
	write(w, 202, op)
}
func (s *Server) externalDatabaseConnections(w http.ResponseWriter, r *http.Request) {
	connections, err := s.Store.ExternalDatabaseConnections(r.Context(), who(r), r.PathValue("id"))
	if err != nil {
		failure(w, err)
		return
	}
	write(w, 200, connections)
}
func (s *Server) externalDatabaseTrust(w http.ResponseWriter, r *http.Request) {
	d, err := s.Store.ExternalDatabase(r.Context(), who(r), r.PathValue("id"), false)
	if err != nil {
		failure(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	write(w, 200, map[string]any{"mode": "required", "hostname": d.Spec.Host, "minimum_protocol": "TLS1.2", "trust_source": "system_ca_bundle", "verified": d.Verified(time.Now().UTC()), "observed_at": d.Observation.ObservedAt, "message": "Use current public certificate authorities and verify this hostname. The provider controls certificate renewal."})
}
func (s *Server) externalDatabaseConnectionPlan(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ApplicationID string `json:"application_id"`
		Service       string `json:"service"`
		Variable      string `json:"variable"`
		Disconnect    bool   `json:"disconnect"`
	}
	if !decode(w, r, &in) {
		return
	}
	plan, err := s.Store.PlanExternalDatabaseConnection(r.Context(), who(r), r.PathValue("id"), in.ApplicationID, in.Service, in.Variable, in.Disconnect)
	if err != nil {
		failure(w, err)
		return
	}
	write(w, 200, plan)
}
func (s *Server) externalDatabaseConnect(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ReviewID           string `json:"review_id"`
		ConfirmApplication string `json:"confirm_application"`
	}
	if !decode(w, r, &in) {
		return
	}
	idem, ok := backupIdempotency(w, r)
	if !ok {
		return
	}
	deployment, err := s.Store.AcceptExternalDatabaseConnection(r.Context(), who(r), r.PathValue("id"), in.ReviewID, in.ConfirmApplication, idem)
	if err != nil {
		failure(w, err)
		return
	}
	write(w, 202, deployment)
}

func (s *Server) observeExternalDatabase(ctx context.Context, d externaldatabase.Resource) externaldatabase.Observation {
	c, err := externaldatabase.OpenCredentials(s.authEncryptionKey(), d)
	if err != nil {
		return externaldatabase.Observation{ObservedAt: time.Now().UTC(), Revision: d.Revision, Status: "unreachable", Message: externaldatabase.ErrUnavailable.Error()}
	}
	return externaldatabase.DefaultProber.Observe(ctx, d, c)
}

// RunExternalDatabases stays in the shared API/reconciler process. Two workers
// claim durable leases; probes have their own process-wide concurrency bound.
func (s *Server) RunExternalDatabases(ctx context.Context) {
	if s.Store == nil {
		return
	}
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			timer := time.NewTicker(time.Second)
			defer timer.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-timer.C:
					step, cancel := context.WithTimeout(ctx, 20*time.Second)
					op, err := s.Store.ClaimExternalDatabaseOperation(step)
					if err == nil {
						d, loadErr := s.Store.ExternalDatabaseInternal(step, op.DatabaseID)
						if loadErr == nil && d.Revision == op.Revision {
							o := externaldatabase.Observation{}
							if op.Kind != "delete" {
								o = s.observeExternalDatabase(step, d)
							}
							_ = s.Store.CompleteExternalDatabaseOperation(step, op, o)
						}
					} else {
						claim, claimErr := s.Store.ClaimExternalDatabaseObservation(step)
						if claimErr == nil {
							_ = s.Store.RecordExternalDatabaseObservation(step, claim, s.observeExternalDatabase(step, claim.Resource))
						}
					}
					cancel()
				}
			}
		}()
	}
	wg.Wait()
}
