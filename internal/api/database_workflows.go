package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"strings"
	"time"

	"github.com/hakopod/hakopod/internal/database"
	"github.com/hakopod/hakopod/internal/spec"
	"github.com/hakopod/hakopod/internal/store"
)

var migrationLockInspectionSlots = make(chan struct{}, 2)

// registerDatabaseWorkflowRoutes is intentionally separate so generated route
// files do not become a prerequisite for the bounded backend slice.
func (s *Server) registerDatabaseWorkflowRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/databases/{id}/application-provisioning-plan", s.databaseApplicationProvisioningPlan)
	mux.HandleFunc("POST /api/v1/databases/{id}/application-provision", s.databaseApplicationProvision)
	mux.HandleFunc("GET /api/v1/database-application-provisioning-operations/{id}", s.databaseApplicationProvisioningOperation)
	mux.HandleFunc("POST /api/v1/databases/{id}/migration-lock-recovery-plan", s.databaseMigrationLockRecoveryPlan)
	mux.HandleFunc("POST /api/v1/databases/{id}/migration-lock-recover", s.databaseMigrationLockRecover)
	mux.HandleFunc("GET /api/v1/database-migration-lock-recovery-operations/{id}", s.databaseMigrationLockRecoveryOperation)
}

func (s *Server) databaseApplicationProvisioningOperation(w http.ResponseWriter, r *http.Request) {
	op, err := s.Store.DatabaseApplicationProvisioningOperation(r.Context(), who(r), r.PathValue("id"))
	if err != nil {
		failure(w, err)
		return
	}
	write(w, 200, op)
}
func (s *Server) databaseMigrationLockRecoveryOperation(w http.ResponseWriter, r *http.Request) {
	op, err := s.Store.MigrationLockRecoveryOperation(r.Context(), who(r), r.PathValue("id"))
	if err != nil {
		failure(w, err)
		return
	}
	write(w, 200, op)
}

func (s *Server) databaseApplicationProvisioningPlan(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ApplicationID   string `json:"application_id"`
		Service         string `json:"service"`
		Variable        string `json:"variable"`
		Endpoint        string `json:"endpoint"`
		Role            string `json:"role"`
		Database        string `json:"database"`
		SecretReference string `json:"secret_reference"`
	}
	if !decode(w, r, &in) {
		return
	}
	if s.Cluster == nil {
		problem(w, 503, "database_controller_unavailable", "The database controller is unavailable.")
		return
	}
	d, err := s.Store.Database(r.Context(), who(r), r.PathValue("id"), true)
	if err != nil {
		failure(w, err)
		return
	}
	a, err := s.Store.Application(r.Context(), in.ApplicationID)
	if err != nil {
		failure(w, err)
		return
	}
	principal := who(r)
	if a.Project != d.Project || a.Environment != d.Environment || !principal.AllowsDatabase(d.Project, d.Environment, true) || !principal.Allows("deployments:write", a.Project, a.Environment, a.Name) {
		problem(w, 403, "forbidden", "The application and database scope is not permitted.")
		return
	}
	secret := in.SecretReference
	if secret == "" {
		secret = "database-" + strings.ReplaceAll(d.Spec.Name, "_", "-") + "-password"
	}
	secrets, err := s.Cluster.ListWorkloadSecrets(r.Context(), d.Project, d.Environment, a.Name)
	if err != nil {
		failure(w, err)
		return
	}
	for _, existing := range secrets {
		if existing.Name == secret {
			problem(w, 409, "secret_reference_conflict", "The selected application secret already exists.")
			return
		}
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		failure(w, err)
		return
	}
	password := []byte(hex.EncodeToString(raw))
	encrypted, err := s.encryptAuth(password)
	if err != nil {
		failure(w, err)
		return
	}
	plan, err := s.Store.PlanDatabaseApplicationProvisioning(r.Context(), who(r), r.PathValue("id"), in.ApplicationID, in.Service, in.Variable, in.Endpoint, in.Role, in.Database, in.SecretReference, encrypted)
	if err != nil {
		failure(w, err)
		return
	}
	write(w, 200, plan)
}

func (s *Server) databaseApplicationProvision(w http.ResponseWriter, r *http.Request) {
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
	op, err := s.Store.AcceptDatabaseApplicationProvisioning(r.Context(), who(r), r.PathValue("id"), in.ReviewID, in.ConfirmApplication, idem)
	if err != nil {
		failure(w, err)
		return
	}
	write(w, 202, op)
}

func (s *Server) databaseMigrationLockRecoveryPlan(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ApplicationID string `json:"application_id"`
		Service       string `json:"service"`
		Variable      string `json:"variable"`
		Profile       string `json:"profile"`
	}
	if !decode(w, r, &in) {
		return
	}
	if s.Cluster == nil {
		problem(w, 503, "database_controller_unavailable", "The database controller is unavailable.")
		return
	}
	select {
	case migrationLockInspectionSlots <- struct{}{}:
		defer func() { <-migrationLockInspectionSlots }()
	default:
		problem(w, 429, "migration_inspection_busy", "Migration inspection capacity is busy. Retry shortly.")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 40*time.Second)
	defer cancel()
	d, err := s.Store.Database(ctx, who(r), r.PathValue("id"), true)
	if err != nil {
		failure(w, err)
		return
	}
	a, err := s.Store.Application(ctx, in.ApplicationID)
	if err != nil {
		failure(w, err)
		return
	}
	principal := who(r)
	if a.Project != d.Project || a.Environment != d.Environment || !principal.AllowsDatabase(d.Project, d.Environment, true) || !principal.Allows("deployments:write", a.Project, a.Environment, a.Name) {
		problem(w, 403, "forbidden", "The application and database scope is not permitted.")
		return
	}
	if in.Profile != database.InfisicalKnexPostgresProfile {
		problem(w, 422, "unsupported_migration_profile", "Only the pinned Infisical Knex PostgreSQL profile is supported.")
		return
	}
	svc, ok := a.Spec.Services[in.Service]
	if !ok {
		problem(w, 400, "invalid_request", "The selected service does not exist.")
		return
	}
	binding, ok := svc.Bindings[in.Variable]
	if !ok || binding.ManagedDatabase != d.ID || binding.Database == "" || svc.Image != "docker.io/infisical/infisical:v0.165.10@sha256:204bd63c7a281d9157752ce0bf8d506e7380cac5a0665324eeab8d580b069266" {
		problem(w, 409, "unsupported_migration_profile", "The pinned Infisical recovery profile requires an explicit managed PostgreSQL database binding.")
		return
	}
	observed, err := s.Cluster.ObserveDatabase(ctx, d)
	if err != nil || observed.Status != "ready" {
		problem(w, 409, "database_not_ready", "The database does not have a current healthy observation.")
		return
	}
	d.Observation = observed
	evidence, err := s.Cluster.InspectInfisicalMigrationLock(ctx, d, a.ID, a.Revision, in.Service, in.Variable, binding.Database, svc.Image)
	if err != nil {
		failure(w, err)
		return
	}
	plan, err := s.Store.SaveMigrationLockRecoveryPlan(ctx, who(r), d.ID, a.ID, in.Service, in.Profile, evidence)
	if err != nil {
		failure(w, err)
		return
	}
	write(w, 200, plan)
}

func (s *Server) databaseMigrationLockRecover(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ReviewID           string `json:"review_id"`
		ConfirmApplication string `json:"confirm_application"`
		ConfirmDatabase    string `json:"confirm_database"`
	}
	if !decode(w, r, &in) {
		return
	}
	idem, ok := backupIdempotency(w, r)
	if !ok {
		return
	}
	op, err := s.Store.AcceptMigrationLockRecovery(r.Context(), who(r), r.PathValue("id"), in.ReviewID, in.ConfirmApplication, in.ConfirmDatabase, idem)
	if err != nil {
		failure(w, err)
		return
	}
	write(w, 202, op)
}

// reconcileDatabaseApplicationProvisioning performs one bounded durable step.
// The server's database worker loop calls it independently from lifecycle work.
func (s *Server) reconcileDatabaseApplicationProvisioning(parent context.Context) {
	claim, cancel := context.WithTimeout(parent, 5*time.Second)
	op, err := s.Store.ClaimDatabaseApplicationProvisioning(claim)
	cancel()
	if err != nil {
		return
	}
	ctx, stop := context.WithTimeout(parent, 40*time.Second)
	defer stop()
	finish := func(status, phase, message string) {
		bounded, done := context.WithTimeout(context.Background(), 3*time.Second)
		defer done()
		_ = s.Store.RecordDatabaseApplicationProvisioning(bounded, op, status, phase, message)
	}
	before := func() error { return s.Store.CheckDatabaseApplicationProvisioning(ctx, op) }
	if err = before(); err != nil {
		finish("cancelled", "authorization", "Provisioning authority or reviewed revisions changed.")
		return
	}
	d, err := s.Store.DatabaseInternal(ctx, op.DatabaseID)
	if err != nil {
		finish("failed", "database", "The reviewed database is unavailable.")
		return
	}
	observed, err := s.Cluster.ObserveDatabase(ctx, d)
	if err != nil || observed.Status != "ready" {
		finish("queued", op.Phase, "Waiting for a healthy PostgreSQL primary.")
		return
	}
	d.Observation = observed
	password, err := s.decryptAuth(op.EncryptedPassword)
	if err != nil {
		finish("failed", "credentials", "Generated credentials could not be opened with the persistent encryption key.")
		return
	}
	defer clear(password)
	verified, err := s.Cluster.ProvisionPostgresApplication(ctx, d, op.Plan, password, op.Phase, before)
	if err != nil {
		finish("failed", "provisioning", "The reviewed PostgreSQL role, database, or privilege verification failed.")
		return
	}
	if !verified {
		finish("queued", "verifying", "The owned PostgreSQL role and database were created. Native privilege verification is queued.")
		return
	}
	if err = before(); err != nil {
		finish("cancelled", "authorization", "Provisioning authority changed before the application secret was written.")
		return
	}
	if err = s.Cluster.PutWorkloadSecret(ctx, d.Project, d.Environment, op.Plan.ApplicationName, op.Plan.SecretReference, string(password)); err != nil {
		finish("queued", op.Phase, "The verified login is ready; retrying the application-scoped secret write.")
		return
	}
	principal, err := s.Store.KeyPrincipal(ctx, op.KeyID)
	if err != nil {
		finish("cancelled", "authorization", "Provisioning authority is no longer valid.")
		return
	}
	connection, err := s.Store.PlanDatabaseConnection(ctx, principal, d.ID, op.Plan.ApplicationID, op.Plan.Service, op.Plan.Variable, op.Plan.Endpoint, false, storeDatabaseConnectionOptions(op.Plan))
	if err != nil {
		finish("failed", "binding", "The application changed before its reviewed binding could be saved.")
		return
	}
	if _, err = s.Store.AcceptProvisionedDatabaseConnection(ctx, principal, d.ID, connection.ID, op.Plan.ApplicationName, "provision-bind-"+op.ID, op.ID, op.Lease); err != nil {
		finish("failed", "binding", "The verified database login could not be bound to the application.")
		return
	}
	// Application revision and terminal operation state commit atomically.
}

func storeDatabaseConnectionOptions(plan database.ApplicationProvisioningPlan) store.DatabaseConnectionOptions {
	return store.DatabaseConnectionOptions{Username: plan.Role, Database: plan.LogicalDatabase, Password: &spec.SecretRef{Ref: plan.SecretReference}, SSLMode: "verify-full"}
}

func (s *Server) reconcileMigrationLockRecovery(parent context.Context) {
	claim, cancel := context.WithTimeout(parent, 5*time.Second)
	op, err := s.Store.ClaimMigrationLockRecovery(claim)
	cancel()
	if err != nil {
		return
	}
	ctx, stop := context.WithTimeout(parent, 40*time.Second)
	defer stop()
	finish := func(after *database.MigrationLockEvidence, status, phase, message string) {
		bounded, done := context.WithTimeout(context.Background(), 3*time.Second)
		defer done()
		_ = s.Store.RecordMigrationLockRecovery(bounded, op, after, status, phase, message)
	}
	before := func() error { return s.Store.CheckMigrationLockRecovery(ctx, op) }
	if err = before(); err != nil {
		finish(nil, "cancelled", "authorization", "Recovery authority or reviewed revisions changed.")
		return
	}
	d, err := s.Store.DatabaseInternal(ctx, op.DatabaseID)
	if err != nil {
		finish(nil, "failed", "database", "The reviewed database is unavailable.")
		return
	}
	observed, err := s.Cluster.ObserveDatabase(ctx, d)
	if err != nil || observed.Status != "ready" {
		finish(nil, "queued", "observing", "Waiting for a healthy PostgreSQL primary.")
		return
	}
	d.Observation = observed
	after, err := s.Cluster.RepairInfisicalMigrationLock(ctx, d, op.Before, before)
	if err != nil {
		finish(&after, "failed", "preconditions", "The lock or runtime evidence changed. Inspect and review again.")
		return
	}
	finish(&after, "succeeded", "verified", "The abandoned Knex lock was released and the unlocked row was verified.")
}

func clear(value []byte) {
	for i := range value {
		value[i] = 0
	}
}
