package api

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/hakopod/hakopod/internal/database"
	"github.com/hakopod/hakopod/internal/store"
)

func (s *Server) registerDatabaseRoutes(mux *http.ServeMux) {
	s.registerDatabasePublicEndpointRoutes(mux)
	mux.HandleFunc("GET /api/v1/database-placement/nodes", s.databasePlacementNodes)
	mux.HandleFunc("POST /api/v1/database-capacity-plan", s.databaseCapacityPlan)
	mux.HandleFunc("GET /api/v1/database-operations/{id}", s.databaseOperation)
	mux.HandleFunc("POST /api/v1/databases/{id}/connection-plan", s.databaseConnectionPlan)
	mux.HandleFunc("POST /api/v1/databases/{id}/connect", s.databaseConnect)
	mux.HandleFunc("POST /api/v1/databases/{id}/inspect", s.databaseInspect)
	mux.HandleFunc("GET /api/v1/databases", s.databases)
	mux.HandleFunc("POST /api/v1/databases", s.createDatabase)
	mux.HandleFunc("GET /api/v1/databases/{id}", s.database)
	mux.HandleFunc("GET /api/v1/databases/{id}/operations", s.databaseOperations)
	mux.HandleFunc("GET /api/v1/databases/{id}/connections", s.databaseConnections)
	mux.HandleFunc("GET /api/v1/databases/{id}/trust", s.databaseTrust)
	mux.HandleFunc("GET /api/v1/databases/{id}/metrics", s.databaseMetricHistory)
	mux.HandleFunc("GET /api/v1/databases/{id}/failures", s.databaseFailureHistory)
	mux.HandleFunc("POST /api/v1/databases/{id}/credentials", s.databaseCredentials)
	mux.HandleFunc("DELETE /api/v1/databases/{id}", s.deleteDatabase)
	mux.HandleFunc("POST /api/v1/databases/{id}/resize-plan", s.databaseResizePlan)
	mux.HandleFunc("POST /api/v1/databases/{id}/resize", s.resizeDatabase)
	mux.HandleFunc("POST /api/v1/databases/{id}/resize-retry-plan", s.databaseResizeRetryPlan)
	mux.HandleFunc("POST /api/v1/databases/{id}/resize-retry", s.databaseResizeRetry)
	mux.HandleFunc("POST /api/v1/databases/{id}/restore-plan", s.managedDatabaseRestorePlan)
	mux.HandleFunc("POST /api/v1/databases/{id}/switchover-plan", s.oracleSwitchoverPlan)
	mux.HandleFunc("POST /api/v1/databases/{id}/switchover", s.oracleSwitchover)
	mux.HandleFunc("POST /api/v1/databases/{id}/switchover-retry", s.oracleSwitchoverRetry)
}

func (s *Server) databaseFailureHistory(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	history, err := s.Store.DatabaseFailureHistory(ctx, who(r), r.PathValue("id"))
	if err != nil {
		failure(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	write(w, http.StatusOK, history)
}

func (s *Server) databaseCapacityPlan(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Project     string        `json:"project"`
		Environment string        `json:"environment"`
		DatabaseID  string        `json:"database_id,omitempty"`
		Spec        database.Spec `json:"spec"`
	}
	if !decode(w, r, &in) {
		return
	}
	if !validScope(in.Project, in.Environment) {
		problem(w, http.StatusBadRequest, "invalid_request", "provide a valid project and environment")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	plan, err := s.Store.PlanDatabaseCapacity(ctx, who(r), in.Project, in.Environment, in.DatabaseID, in.Spec)
	if err != nil {
		failure(w, err)
		return
	}
	write(w, http.StatusOK, plan)
}

type databaseResizeRetryRuntime interface {
	DatabaseControllerAvailable(context.Context, database.Spec) error
	DatabaseRevisionApplied(context.Context, database.Resource) (bool, error)
	ObserveDatabase(context.Context, database.Resource) (database.Observation, error)
	ValidateDatabaseResize(context.Context, database.Resource, database.Spec) error
}

func (s *Server) resizeRetryRuntime() databaseResizeRetryRuntime {
	if s.databaseResizeRetryTestRuntime != nil {
		return s.databaseResizeRetryTestRuntime
	}
	if s.Cluster == nil {
		return nil
	}
	return s.Cluster
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
	in.Spec = in.Spec.WithSecureDefaults()
	if err := in.Spec.Validate(); err != nil {
		problem(w, 400, "invalid_request", err.Error())
		return
	}
	if in.Spec.Engine == "duckdb" && !database.MyDuckRuntimeQualified {
		problem(w, 503, "database_controller_unavailable", "DuckDB (MyDuck) creation is unavailable until its pinned source, dual-protocol TLS and single-node lifecycle pass native qualification.")
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
func (s *Server) databaseConnections(w http.ResponseWriter, r *http.Request) {
	items, err := s.Store.DatabaseConnections(r.Context(), who(r), r.PathValue("id"))
	if err != nil {
		failure(w, err)
		return
	}
	write(w, http.StatusOK, items)
}
func (s *Server) databaseTrust(w http.ResponseWriter, r *http.Request) {
	d, err := s.Store.Database(r.Context(), who(r), r.PathValue("id"), false)
	if err != nil {
		failure(w, err)
		return
	}
	if s.Cluster == nil || !d.Spec.TLSRequired() {
		problem(w, 409, "database_tls_unavailable", "Verified database TLS has not been configured.")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	trust, err := s.Cluster.DatabaseTrust(ctx, d)
	if err != nil {
		problem(w, 503, "database_trust_unavailable", err.Error())
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	write(w, http.StatusOK, trust)
}
func (s *Server) databaseCredentials(w http.ResponseWriter, r *http.Request) {
	d, err := s.Store.Database(r.Context(), who(r), r.PathValue("id"), true)
	if err != nil {
		failure(w, err)
		return
	}
	if !agentScopedCredentials(w, r, d.Project, d.Environment) {
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
	username, name := databaseCredentialDefaults(d)
	write(w, 200, map[string]string{"username": username, "password": string(password), "database": name})
}

func databaseCredentialDefaults(d database.Resource) (string, string) {
	username, name := "app", "app"
	if d.Spec.Engine == "duckdb" {
		username = "root"
	}
	if d.Spec.Engine == "oracle" {
		username, name = "APP", "FREEPDB1"
		if d.Spec.Oracle != nil && d.Spec.Oracle.Edition == "enterprise" {
			name = "APPDB"
		}
	}
	if d.Spec.Engine == "redis" {
		username = "default"
		name = "0"
	}
	return username, name
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
	var workers sync.WaitGroup
	start := func(interval time.Duration, step func(context.Context)) {
		workers.Add(1)
		go func() {
			defer workers.Done()
			timer := time.NewTicker(interval)
			defer timer.Stop()
			for ctx.Err() == nil {
				step(ctx)
				select {
				case <-ctx.Done():
					return
				case <-timer.C:
				}
			}
		}()
	}
	// A slow engine probe must not occupy the lifecycle reconciliation lane.
	// Durable claims prevent two observation workers from probing the same DB.
	start(time.Second, s.reconcileDatabase)
	start(time.Second, s.reconcileDatabasePublicEndpoint)
	for range 4 {
		start(time.Second, s.refreshDatabaseObservation)
	}
	start(30*time.Second, func(parent context.Context) {
		bounded, cancel := context.WithTimeout(parent, 5*time.Second)
		defer cancel()
		_ = s.Store.RefreshDatabaseRecoveries(bounded)
		_ = s.Store.PruneDatabaseMetrics(bounded)
		_ = s.Store.ExpireDatabasePublicEndpointReviews(bounded)
	})
	workers.Wait()
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
	names, err := s.Store.DatabasePublicEndpointNames(ctx, d.ID, "")
	if err != nil {
		return
	}
	d.PublicEndpointNames = names
	d.PublicEndpointMembers, err = s.Store.DatabasePublicEndpointMembers(ctx, d.ID, "")
	if err != nil {
		return
	}
	d.PublicEndpointAccess, err = s.Store.DatabasePublicEndpointAccessRequired(ctx, d.ID)
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
	if op.Kind == "switchover" {
		s.reconcileOracleSwitchover(ctx, d, op, before, finish)
		return
	}
	password, err := database.OpenCredentials(s.authEncryptionKey(), d)
	if err != nil {
		finish("failed", "credentials", "Database credentials could not be opened with the persistent encryption key.", d.Observation)
		return
	}
	if op.Kind == "resize" || op.Kind == "resize-retry" {
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
	reconcileDatabaseProvisioning(ctx, s.Cluster, d, op.Phase, password, before, finish)
}

type databaseProvisioningRuntime interface {
	ApplyDatabase(context.Context, database.Resource, []byte, func() error) error
	ObserveDatabase(context.Context, database.Resource) (database.Observation, error)
	ReconcileVitessBackupAuthority(context.Context, database.Resource, func() error) error
}

func reconcileDatabaseProvisioning(ctx context.Context, runtime databaseProvisioningRuntime, d database.Resource, phase string, password []byte, before func() error, finish func(string, string, string, database.Observation)) {
	// Vitess and Oracle reconciliation and health checks each need their own bounded step.
	// Persist the handoff so a process restart cannot spend the observation budget
	// applying the same resources again. A pending observation returns to apply.
	splitObservation := d.Spec.Engine == "vitess" || d.Spec.Engine == "oracle"
	if !splitObservation || phase != "observing" {
		if err := runtime.ApplyDatabase(ctx, d, password, before); err != nil {
			finish("failed", "reconcile", "Database resources could not be reconciled. Verify controller availability, capacity and ownership.", d.Observation)
			return
		}
		if splitObservation {
			finish("queued", "observing", "Waiting for database health checks.", d.Observation)
			return
		}
	} else if d.Spec.Engine == "vitess" {
		// A restart may load different storage approvals between the two steps.
		if err := runtime.ReconcileVitessBackupAuthority(ctx, d, before); err != nil {
			finish("failed", "reconcile", "Vitess native backup authority could not be verified.", d.Observation)
			return
		}
	}
	observed, err := runtime.ObserveDatabase(ctx, d)
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

func (s *Server) databaseResizeRetryPlan(w http.ResponseWriter, r *http.Request) {
	var in struct {
		OperationID      string `json:"operation_id"`
		ExpectedRevision int64  `json:"expected_revision"`
	}
	if !decode(w, r, &in) {
		return
	}
	d, err := s.Store.Database(r.Context(), who(r), r.PathValue("id"), true)
	if err != nil {
		failure(w, err)
		return
	}
	source, err := s.Store.LatestDatabaseResizeOperation(r.Context(), who(r), d.ID)
	if err != nil {
		failure(w, err)
		return
	}
	if source.ID != in.OperationID || d.Status != "failed" || d.Revision != in.ExpectedRevision || source.DatabaseID != d.ID || source.Revision != d.Revision || source.Status != "failed" || (source.Kind != "resize" && source.Kind != "resize-retry") || source.Review == nil || !source.Spec.Equal(d.Spec) || (d.Spec.Engine != "mysql" && d.Spec.Engine != "mongodb") || d.Spec.Mode != "cluster" {
		problem(w, http.StatusConflict, "database_resize_retry_unavailable", "Only the current failed MySQL or MongoDB replica change can be reviewed for retry.")
		return
	}
	runtime := s.resizeRetryRuntime()
	if runtime == nil {
		problem(w, http.StatusServiceUnavailable, "database_controller_unavailable", "The database controller is unavailable.")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	if err = runtime.DatabaseControllerAvailable(ctx, d.Spec); err != nil {
		problem(w, http.StatusServiceUnavailable, "database_controller_unavailable", err.Error())
		return
	}
	applied, err := runtime.DatabaseRevisionApplied(ctx, d)
	if err != nil {
		problem(w, http.StatusConflict, "database_resize_retry_unavailable", err.Error())
		return
	}
	resize := *source.Review
	state := "accepted"
	if applied {
		observed, observeErr := runtime.ObserveDatabase(ctx, d)
		resize.ExpiresAt = time.Now().UTC().Add(database.ReviewLifetime)
		// This sentinel cannot equal a native prior-topology fingerprint. If the
		// accepted controller revision rolls back before reconciliation, the
		// retry fails review instead of replaying a stale topology.
		resize.TopologyFingerprint = fmt.Sprintf("accepted-revision:%d:%s", d.Revision, observed.TopologyFingerprint)
		resize.BlockedReasons = []string{}
		if observeErr != nil || observed.Status != "ready" {
			resize.Warnings = append(resize.Warnings, "The controller has the accepted revision, but current native health is incomplete or unverified. Retry reconciles the same desired replica change and succeeds only after full native readiness.")
		}
	} else {
		state = "prior"
		prior := d
		prior.Spec = source.Review.Current
		prior.Revision = source.Review.ExpectedRevision
		prior.Status = "ready"
		observed, observeErr := runtime.ObserveDatabase(ctx, prior)
		if observeErr != nil || observed.Status != "ready" {
			problem(w, http.StatusConflict, "database_resize_retry_unavailable", "The prior reviewed database configuration is not fully healthy.")
			return
		}
		prior.Observation = observed
		evidence, evidenceErr := s.Store.DatabaseBackupEvidence(ctx, prior.ID, prior.Revision)
		if evidenceErr != nil {
			failure(w, evidenceErr)
			return
		}
		resize, err = database.PlanResize(prior, d.Spec, evidence, time.Now().UTC())
		if err == nil {
			err = runtime.ValidateDatabaseResize(ctx, prior, d.Spec)
		}
		if err != nil || len(resize.BlockedReasons) > 0 {
			problem(w, http.StatusConflict, "database_resize_retry_unavailable", "Database health, topology or backup evidence no longer permits this replica change.")
			return
		}
	}
	plan := database.ResizeRetryReview{OperationID: source.ID, DatabaseID: d.ID, Revision: d.Revision, State: state, Resize: resize, ExpiresAt: resize.ExpiresAt}
	id, err := s.Store.SaveDatabaseReview(ctx, who(r), d, "resize-retry", plan, plan.ExpiresAt)
	if err != nil {
		failure(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	write(w, http.StatusOK, map[string]any{"id": id, "plan": plan})
}

func (s *Server) databaseResizeRetry(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ReviewID         string `json:"review_id"`
		OperationID      string `json:"operation_id"`
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
		problem(w, http.StatusBadRequest, "invalid_request", "confirm_name must match the database name; retry resumes the same replica change")
		return
	}
	idem, ok := backupIdempotency(w, r)
	if !ok {
		return
	}
	op, err := s.Store.AcceptDatabaseResizeRetry(r.Context(), who(r), d.ID, in.OperationID, in.ReviewID, in.ExpectedRevision, idem)
	if err != nil {
		failure(w, err)
		return
	}
	write(w, http.StatusAccepted, op)
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
	// Finish inside the 60-second observation lease. Scope lookup gets five
	// seconds; both maintenance passes share twenty. This leaves the full
	// 25-second health budget plus time to release maintenance and save results.
	ctx, cancel := context.WithTimeout(parent, 58*time.Second)
	defer cancel()
	lookup, stopLookup := context.WithTimeout(ctx, 5*time.Second)
	defer stopLookup()
	d, lease, err := s.Store.ClaimDatabaseObservation(lookup)
	if err != nil {
		return
	}
	defer s.Store.ReleaseDatabaseObservation(d.ID, lease)
	names, err := s.Store.DatabasePublicEndpointNames(lookup, d.ID, "")
	if err != nil {
		return
	}
	d.PublicEndpointNames = names
	d.PublicEndpointMembers, err = s.Store.DatabasePublicEndpointMembers(lookup, d.ID, "")
	if err != nil {
		return
	}
	d.PublicEndpointAccess, err = s.Store.DatabasePublicEndpointAccessRequired(lookup, d.ID)
	if err != nil {
		return
	}
	stopLookup()
	maintenance, stopMaintenance := context.WithTimeout(ctx, 20*time.Second)
	defer stopMaintenance()
	var identityErr, networkPolicyErr, nativeStorageErr error
	if d.Spec.Engine == "vitess" {
		claim, e := s.Store.ClaimDatabaseNativeStorageMaintenance(maintenance, d.ID, d.Revision)
		if e != nil {
			nativeStorageErr = e
		} else if claim == nil {
			nativeStorageErr = store.ErrConflict
		} else {
			nativeStorageErr = s.Cluster.ReconcileVitessBackupAuthority(maintenance, d, func() error { return claim.Check(maintenance) })
			claim.Release()
		}
	}
	renewIdentity := d.Status == "ready" && nativeStorageErr == nil && (d.Spec.Engine == "redis" || d.Spec.Engine == "mysql" || d.Spec.Engine == "mongodb" || d.Spec.Engine == "clickhouse" || d.Spec.Engine == "oracle" || d.Spec.Engine == "vitess" || d.Spec.Engine == "duckdb") && d.Spec.TLSRequired()
	// Lifecycle readiness, not observed health, permits maintenance: stale API
	// endpoints can prevent an otherwise ready database from becoming healthy.
	reconcileNetworkPolicy := d.Status == "ready"
	if renewIdentity || reconcileNetworkPolicy {
		claim, e := s.Store.ClaimDatabaseMaintenance(maintenance, d.ID, d.Revision)
		if e != nil {
			networkPolicyErr = e
		} else if claim != nil {
			if renewIdentity {
				identityErr = s.Cluster.RenewDatabaseIdentity(maintenance, d, func() error { return claim.Check(maintenance) })
			}
			if reconcileNetworkPolicy {
				networkPolicyErr = s.Cluster.ReconcileDatabaseNetworkPolicy(maintenance, d, func() error { return claim.Check(maintenance) })
			}
			claim.Release()
		}
	}
	stopMaintenance()
	observe, stop := context.WithTimeout(ctx, 25*time.Second)
	defer stop()
	o, err := s.Cluster.ObserveDatabase(observe, d)
	if err != nil {
		o.Status = "unknown"
		o.Message = "Database health could not be verified."
	}
	if identityErr != nil && o.TLS != nil {
		o.TLS.Message = "Automatic certificate renewal could not complete. Check controller availability and certificate expiry."
	}
	if networkPolicyErr != nil {
		o.Status = "unknown"
		o.Message = "Database network policy could not be refreshed. Reconciliation will retry."
	}
	if nativeStorageErr != nil || d.Spec.Engine == "vitess" && identityErr != nil {
		o.Status = "unknown"
		o.Message = "Vitess recovery storage authority or certificate maintenance could not be verified. Reconciliation will retry."
	}
	_ = s.Store.ObserveClaimedDatabase(ctx, d.ID, d.Revision, lease, o)
}
