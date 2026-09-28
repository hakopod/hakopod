package api

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"time"

	"github.com/hakopod/hakopod/internal/database"
	"github.com/hakopod/hakopod/internal/store"
)

func (s *Server) registerDatabaseRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/database-operations/{id}", s.databaseOperation)
	mux.HandleFunc("POST /api/v1/databases/{id}/connection-plan", s.databaseConnectionPlan)
	mux.HandleFunc("POST /api/v1/databases/{id}/connect", s.databaseConnect)
	mux.HandleFunc("POST /api/v1/databases/{id}/inspect", s.databaseInspect)
	mux.HandleFunc("GET /api/v1/databases", s.databases)
	mux.HandleFunc("POST /api/v1/databases", s.createDatabase)
	mux.HandleFunc("GET /api/v1/databases/{id}", s.database)
	mux.HandleFunc("GET /api/v1/databases/{id}/operations", s.databaseOperations)
	mux.HandleFunc("POST /api/v1/databases/{id}/credentials", s.databaseCredentials)
	mux.HandleFunc("DELETE /api/v1/databases/{id}", s.deleteDatabase)
	mux.HandleFunc("POST /api/v1/databases/{id}/resize-plan", s.databaseResizePlan)
	mux.HandleFunc("POST /api/v1/databases/{id}/resize", s.resizeDatabase)
	mux.HandleFunc("POST /api/v1/databases/{id}/restore-plan", s.managedDatabaseRestorePlan)
}
func (s *Server) databaseOperation(w http.ResponseWriter, r *http.Request) {
	op, err := s.Store.DatabaseOperation(r.Context(), who(r), r.PathValue("id"))
	if err != nil {
		failure(w, err)
		return
	}
	write(w, http.StatusOK, op)
}
func (s *Server) databases(w http.ResponseWriter, r *http.Request) {
	project, environment := scope(r)
	if !validScope(project, environment) {
		problem(w, 400, "invalid_request", "select a project and environment")
		return
	}
	items, err := s.Store.Databases(r.Context(), who(r), project, environment)
	if err != nil {
		failure(w, err)
		return
	}
	write(w, 200, map[string]any{"items": items})
}
func (s *Server) createDatabase(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Project     string        `json:"project"`
		Environment string        `json:"environment"`
		Spec        database.Spec `json:"spec"`
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
	if err := in.Spec.Validate(); err != nil {
		problem(w, 400, "invalid_request", err.Error())
		return
	}
	if len(s.authEncryptionKey()) != 32 {
		problem(w, 503, "unavailable", "configure the persistent encryption key before creating a database")
		return
	}
	if err := s.Cluster.DatabaseControllerAvailable(r.Context(), in.Spec); err != nil {
		problem(w, 503, "database_controller_unavailable", err.Error())
		return
	}
	if err := s.Cluster.ValidateDatabasePlacement(r.Context(), database.Resource{Project: in.Project, Environment: in.Environment, Spec: in.Spec}); err != nil {
		problem(w, 503, "database_placement_unavailable", err.Error())
		return
	}
	idem, ok := backupIdempotency(w, r)
	if !ok {
		return
	}
	// Bind a retried creation to the same identity and resource. The password is
	// sealed once; idempotent replays return the first accepted operation.
	sum := sha256.Sum256([]byte("managed-database:" + who(r).ID + ":" + idem))
	id := hex.EncodeToString(sum[:16])
	random := make([]byte, 32)
	if _, err := rand.Read(random); err != nil {
		failure(w, err)
		return
	}
	password := []byte(hex.EncodeToString(random))
	sealed, err := database.SealCredentials(s.authEncryptionKey(), id, password)
	if err != nil {
		failure(w, err)
		return
	}
	op, err := s.Store.AcceptDatabase(r.Context(), who(r), database.Resource{ID: id, Project: in.Project, Environment: in.Environment, Spec: in.Spec, EncryptedCredentials: sealed}, 0, idem, "create")
	if err != nil {
		failure(w, err)
		return
	}
	write(w, 202, op)
}
func (s *Server) database(w http.ResponseWriter, r *http.Request) {
	d, err := s.Store.Database(r.Context(), who(r), r.PathValue("id"), false)
	if err != nil {
		failure(w, err)
		return
	}
	write(w, 200, d)
}
func (s *Server) databaseOperations(w http.ResponseWriter, r *http.Request) {
	items, err := s.Store.DatabaseOperations(r.Context(), who(r), r.PathValue("id"))
	if err != nil {
		failure(w, err)
		return
	}
	write(w, 200, map[string]any{"items": items})
}
func (s *Server) databaseCredentials(w http.ResponseWriter, r *http.Request) {
	d, err := s.Store.Database(r.Context(), who(r), r.PathValue("id"), true)
	if err != nil {
		failure(w, err)
		return
	}
	if err = s.Store.AuditDatabaseCredentials(r.Context(), who(r), d); err != nil {
		failure(w, err)
		return
	}
	private, err := s.Store.DatabaseInternal(r.Context(), d.ID)
	if err != nil {
		failure(w, err)
		return
	}
	password, err := database.OpenCredentials(s.authEncryptionKey(), private)
	if err != nil {
		failure(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	username := "app"
	name := "app"
	if d.Spec.Engine == "redis" {
		username = "default"
		name = "0"
	}
	write(w, 200, map[string]string{"username": username, "password": string(password), "database": name})
}
func (s *Server) deleteDatabase(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ExpectedRevision int64  `json:"expected_revision"`
		ConfirmName      string `json:"confirm_name"`
	}
	if !decode(w, r, &in) {
		return
	}
	d, err := s.Store.Database(r.Context(), who(r), r.PathValue("id"), true)
	if err != nil {
		failure(w, err)
		return
	}
	if in.ConfirmName != d.Spec.Name {
		problem(w, 400, "invalid_request", "confirm_name must match the database name; deletion removes its data")
		return
	}
	idem, ok := backupIdempotency(w, r)
	if !ok {
		return
	}
	op, err := s.Store.AcceptDatabase(r.Context(), who(r), d, in.ExpectedRevision, idem, "delete")
	if err != nil {
		failure(w, err)
		return
	}
	write(w, 202, op)
}
func (s *Server) databaseResizePlan(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Spec database.Spec `json:"spec"`
	}
	if !decode(w, r, &in) {
		return
	}
	d, err := s.Store.Database(r.Context(), who(r), r.PathValue("id"), true)
	if err != nil {
		failure(w, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	if s.Cluster != nil {
		observed, e := s.Cluster.ObserveDatabase(ctx, d)
		if e == nil {
			d.Observation = observed
			_ = s.Store.ObserveDatabase(ctx, d.ID, d.Revision, observed)
		}
	}
	evidence, err := s.Store.DatabaseBackupEvidence(r.Context(), d.ID, d.Revision)
	if err != nil {
		failure(w, err)
		return
	}
	plan, err := database.PlanResize(d, in.Spec, evidence, time.Now().UTC())
	if err != nil {
		problem(w, 400, "invalid_request", err.Error())
		return
	}
	if s.Cluster == nil {
		plan.BlockedReasons = append(plan.BlockedReasons, "Database controller is unavailable.")
	} else if e := s.Cluster.ValidateDatabaseResize(ctx, d, in.Spec); e != nil {
		plan.BlockedReasons = append(plan.BlockedReasons, e.Error())
	}
	id, err := s.Store.SaveDatabaseReview(r.Context(), who(r), d, "resize", plan, plan.ExpiresAt)
	if err != nil {
		failure(w, err)
		return
	}
	write(w, 200, map[string]any{"id": id, "plan": plan})
}

func (s *Server) RunManagedDatabases(ctx context.Context) {
	if s.Cluster == nil || s.Store == nil {
		return
	}
	timer := time.NewTicker(time.Second)
	defer timer.Stop()
	for ctx.Err() == nil {
		s.reconcileDatabase(ctx)
		s.refreshDatabaseObservation(ctx)
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
	}
}
func (s *Server) reconcileDatabase(parent context.Context) {
	claim, cancel := context.WithTimeout(parent, 5*time.Second)
	op, err := s.Store.ClaimDatabaseOperation(claim)
	cancel()
	if err != nil {
		return
	}
	ctx, stop := context.WithTimeout(parent, 25*time.Second)
	defer stop()
	d, err := s.Store.DatabaseInternal(ctx, op.DatabaseID)
	if err != nil {
		return
	}
	finish := func(status, phase, message string, o database.Observation) {
		finish, c := context.WithTimeout(context.Background(), 3*time.Second)
		defer c()
		_ = s.Store.RecordDatabaseStep(finish, op, o, status, phase, message)
	}
	before := func() error { return s.Store.CheckDatabaseOperation(ctx, op) }
	if err = before(); err != nil {
		if errors.Is(err, store.ErrForbidden) || errors.Is(err, store.ErrUnauthorized) {
			finish("cancelled", "authorization", "Database operation authority is no longer valid.", d.Observation)
		}
		return
	}
	if op.StartedAt != nil && time.Since(*op.StartedAt) > 30*time.Minute {
		finish("failed", "timeout", "The database did not complete its operation within 30 minutes. Inspect controller events before retrying.", d.Observation)
		return
	}
	if op.Kind == "delete" {
		done, e := s.Cluster.DeleteDatabase(ctx, d, before)
		if e != nil {
			finish("failed", "delete", "Database deletion could not proceed. Verify controller availability and ownership.", d.Observation)
			return
		}
		if done {
			finish("succeeded", "deleted", "", d.Observation)
		} else {
			finish("queued", "deleting", "Waiting for owned database resources to be removed.", d.Observation)
		}
		return
	}
	password, err := database.OpenCredentials(s.authEncryptionKey(), d)
	if err != nil {
		finish("failed", "credentials", "Database credentials could not be opened with the persistent encryption key.", d.Observation)
		return
	}
	if op.Kind == "resize" {
		applied, e := s.Cluster.DatabaseRevisionApplied(ctx, d)
		if e != nil {
			finish("queued", "checking", "Waiting for the database controller.", d.Observation)
			return
		}
		if !applied {
			if op.Review == nil || !op.Review.ExpiresAt.After(time.Now()) {
				finish("failed", "review", "The resize review expired before the change started. Review the database again.", d.Observation)
				return
			}
			current := d
			current.Spec = op.Review.Current
			current.Revision = op.Review.ExpectedRevision
			current.Status = "ready"
			current.Observation, e = s.Cluster.ObserveDatabase(ctx, current)
			evidence, backupErr := s.Store.DatabaseBackupEvidence(ctx, current.ID, current.Revision)
			plan, planErr := database.PlanResize(current, d.Spec, evidence, time.Now().UTC())
			if planErr == nil {
				planErr = s.Cluster.ValidateDatabaseResize(ctx, current, d.Spec)
			}
			if e != nil || backupErr != nil || planErr != nil || len(plan.BlockedReasons) > 0 || plan.TopologyFingerprint != op.Review.TopologyFingerprint {
				finish("failed", "review", "Database health, topology or backup evidence changed. Review the resize again.", d.Observation)
				return
			}
		}
	}
	if err = s.Cluster.ApplyDatabase(ctx, d, password, before); err != nil {
		finish("failed", "reconcile", "Database resources could not be reconciled. Verify controller availability, capacity and ownership.", d.Observation)
		return
	}
	observed, err := s.Cluster.ObserveDatabase(ctx, d)
	if err != nil {
		observed.Status = "pending"
		observed.Message = "Waiting for a verified database observation."
	}
	if observed.Status == "ready" {
		finish("succeeded", "ready", "", observed)
	} else {
		finish("queued", "provisioning", observed.Message, observed)
	}
}

func (s *Server) resizeDatabase(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ReviewID         string        `json:"review_id"`
		ExpectedRevision int64         `json:"expected_revision"`
		Spec             database.Spec `json:"spec"`
	}
	if !decode(w, r, &in) {
		return
	}
	d, err := s.Store.Database(r.Context(), who(r), r.PathValue("id"), true)
	if err != nil {
		failure(w, err)
		return
	}
	idem, ok := backupIdempotency(w, r)
	if !ok {
		return
	}
	d.Spec = in.Spec
	op, err := s.Store.AcceptDatabaseResize(r.Context(), who(r), d, in.ExpectedRevision, idem, in.ReviewID)
	if err != nil {
		failure(w, err)
		return
	}
	write(w, 202, op)
}
func (s *Server) refreshDatabaseObservation(parent context.Context) {
	ctx, cancel := context.WithTimeout(parent, 10*time.Second)
	defer cancel()
	_ = s.Store.RefreshDatabaseRecoveries(ctx)
	d, err := s.Store.NextDatabaseObservation(ctx)
	if err != nil {
		return
	}
	o, err := s.Cluster.ObserveDatabase(ctx, d)
	if err != nil {
		o.Status = "unknown"
		o.Message = "Database health could not be verified."
	}
	_ = s.Store.ObserveDatabase(ctx, d.ID, d.Revision, o)
}
