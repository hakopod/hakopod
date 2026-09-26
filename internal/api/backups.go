package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/hakopod/hakopod/internal/backup"
	"github.com/hakopod/hakopod/internal/store"
)

func (s *Server) registerBackupRoutes(mux *http.ServeMux) {
	routes := map[string]http.HandlerFunc{
		"GET /api/v1/backup-destinations": s.backupDestinations, "POST /api/v1/backup-destinations": s.putBackupDestination, "PUT /api/v1/backup-destinations/{id}": s.putBackupDestination, "DELETE /api/v1/backup-destinations/{id}": s.deleteBackupDestination, "POST /api/v1/backup-destinations/{id}/test": s.testBackupDestination,
		"GET /api/v1/backup-targets": s.backupTargets, "GET /api/v1/backups": s.backupJobs, "POST /api/v1/backups": s.createBackup, "GET /api/v1/backups/{id}": s.backupJob, "POST /api/v1/backups/{id}/cancel": s.cancelBackup,
		"GET /api/v1/backup-artifacts": s.backupArtifacts, "GET /api/v1/backup-artifacts/{id}": s.backupArtifact, "DELETE /api/v1/backup-artifacts/{id}": s.deleteBackupArtifact, "POST /api/v1/backup-artifacts/{id}/restore-plan": s.planBackupRestore, "POST /api/v1/backup-artifacts/{id}/restore": s.restoreBackup,
		"GET /api/v1/backup-schedules": s.backupSchedules, "POST /api/v1/backup-schedules": s.putBackupSchedule, "PUT /api/v1/backup-schedules/{id}": s.putBackupSchedule, "DELETE /api/v1/backup-schedules/{id}": s.deleteBackupSchedule,
	}
	for path, handler := range routes {
		mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
			if !who(r).CanManageBackups() {
				problem(w, 403, "forbidden", "Database backups require write access to the selected project and environment.")
				return
			}
			if s.Backups == nil {
				problem(w, 503, "backup_unavailable", "Backup worker is not configured.")
				return
			}
			handler(w, r.WithContext(store.WithBackupPrincipal(r.Context(), who(r))))
		})
	}
}
func backupFailure(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, backup.ErrInput):
		problem(w, 400, "invalid_backup", err.Error())
	case errors.Is(err, backup.ErrConflict):
		problem(w, 409, "backup_conflict", err.Error())
	case errors.Is(err, backup.ErrNotFound):
		problem(w, 404, "not_found", "Backup resource not found.")
	default:
		failure(w, err)
	}
}
func (s *Server) backupEncryption(w http.ResponseWriter) bool {
	if len(s.Backups.CredentialKey) != 32 {
		problem(w, 503, "backup_unavailable", "Configure the persistent authentication encryption key before storing backup credentials.")
		return false
	}
	return true
}
func backupIdempotency(w http.ResponseWriter, r *http.Request) (string, bool) {
	key := r.Header.Get("Idempotency-Key")
	if len(key) < 8 || len(key) > 128 {
		problem(w, 400, "invalid_request", "Idempotency-Key must contain 8–128 characters.")
		return "", false
	}
	return key, true
}
func (s *Server) backupDestinations(w http.ResponseWriter, r *http.Request) {
	items, err := s.Store.BackupDestinations(r.Context())
	if err != nil {
		backupFailure(w, err)
		return
	}
	write(w, 200, map[string]any{"items": items})
}
func (s *Server) putBackupDestination(w http.ResponseWriter, r *http.Request) {
	if !s.backupEncryption(w) {
		return
	}
	var in backup.DestinationInput
	if !decode(w, r, &in) {
		return
	}
	if err := in.Validate(); err != nil {
		backupFailure(w, err)
		return
	}
	if !who(r).IsAdmin() && (in.AllowHTTP || !strings.HasPrefix(in.Endpoint, "https://")) {
		backupFailure(w, fmt.Errorf("%w: workspace backups require a public HTTPS endpoint", backup.ErrInput))
		return
	}
	d := backup.Destination{Project: who(r).Project, Environment: who(r).Environment, ID: r.PathValue("id"), Name: strings.TrimSpace(in.Name), Endpoint: strings.TrimSuffix(in.Endpoint, "/"), Region: in.Region, Bucket: in.Bucket, Prefix: strings.TrimSuffix(in.Prefix, "/"), PathStyle: in.PathStyle, AllowHTTP: in.AllowHTTP}
	var credentials backup.Credentials
	var recovery string
	if r.Method == "POST" {
		if in.ExpectedRevision != 0 {
			backupFailure(w, backup.ErrConflict)
			return
		}
		if in.AccessKeyID == "" || in.SecretAccessKey == "" {
			backupFailure(w, fmt.Errorf("%w: access_key_id and secret_access_key are required", backup.ErrInput))
			return
		}
		d.ID = store.NewID()
		identity, recipient, err := backup.NewEncryptionIdentity(in.EncryptionIdentity)
		if err != nil {
			backupFailure(w, err)
			return
		}
		d.EncryptionRecipient = recipient
		credentials = backup.Credentials{AccessKeyID: in.AccessKeyID, SecretAccessKey: in.SecretAccessKey, SessionToken: in.SessionToken, EncryptionIdentity: identity}
		if in.EncryptionIdentity == "" {
			recovery = identity
		}
	} else {
		old, err := s.Store.BackupDestination(r.Context(), d.ID)
		if err != nil {
			backupFailure(w, err)
			return
		}
		if old.Revision != in.ExpectedRevision {
			backupFailure(w, backup.ErrConflict)
			return
		}
		if old.Endpoint != d.Endpoint || old.Region != d.Region || old.Bucket != d.Bucket || old.Prefix != d.Prefix || old.PathStyle != d.PathStyle || old.AllowHTTP != d.AllowHTTP {
			backupFailure(w, fmt.Errorf("%w: storage location is immutable; create a separate destination", backup.ErrInput))
			return
		}
		credentials, err = backup.OpenCredentials(s.Backups.CredentialKey, old)
		if err != nil {
			backupFailure(w, err)
			return
		}
		d.Project = old.Project
		d.Environment = old.Environment
		d.EncryptionRecipient = old.EncryptionRecipient
		if in.EncryptionIdentity != "" && in.EncryptionIdentity != credentials.EncryptionIdentity {
			backupFailure(w, fmt.Errorf("%w: encryption identity is immutable", backup.ErrInput))
			return
		}
		if in.AccessKeyID != "" || in.SecretAccessKey != "" {
			if in.AccessKeyID == "" || in.SecretAccessKey == "" {
				backupFailure(w, fmt.Errorf("%w: rotate access key and secret together", backup.ErrInput))
				return
			}
			credentials.AccessKeyID = in.AccessKeyID
			credentials.SecretAccessKey = in.SecretAccessKey
			credentials.SessionToken = in.SessionToken
		}
	}
	d.CredentialRef = "backup:" + d.ID
	sealed, err := backup.SealCredentials(s.Backups.CredentialKey, d.ID, credentials)
	if err != nil {
		backupFailure(w, err)
		return
	}
	d.EncryptedCredentials = sealed
	d, err = s.Store.PutBackupDestination(r.Context(), who(r), d, in.ExpectedRevision)
	if err != nil {
		backupFailure(w, err)
		return
	}
	if r.Method == "POST" {
		response := map[string]any{"destination": d}
		if recovery != "" {
			response["recovery_key"] = recovery
		}
		write(w, 201, response)
	} else {
		write(w, 200, d)
	}
}
func (s *Server) deleteBackupDestination(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ExpectedRevision int64 `json:"expected_revision"`
	}
	if !decode(w, r, &in) {
		return
	}
	if err := s.Store.DeleteBackupDestination(r.Context(), who(r), r.PathValue("id"), in.ExpectedRevision); err != nil {
		backupFailure(w, err)
		return
	}
	write(w, 200, map[string]bool{"deleted": true})
}
func (s *Server) testBackupDestination(w http.ResponseWriter, r *http.Request) {
	if !s.backupEncryption(w) {
		return
	}
	var in struct{}
	if !decode(w, r, &in) {
		return
	}
	d, err := s.Store.BackupDestination(r.Context(), r.PathValue("id"))
	if err != nil {
		backupFailure(w, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	n, err := s.Backups.TestDestination(ctx, d)
	if err != nil {
		if errors.Is(err, backup.ErrConflict) {
			backupFailure(w, err)
		} else {
			problem(w, 502, "storage_test_failed", "S3 write/read/checksum/delete test failed; verify endpoint, credentials and bucket permissions.")
		}
		return
	}
	write(w, 200, map[string]any{"ok": true, "bytes": n, "message": "S3 object write, read-back SHA-256 and deletion succeeded."})
}
func (s *Server) backupTargets(w http.ResponseWriter, r *http.Request) {
	items, err := s.Backups.Runtime.Targets(r.Context())
	if err != nil {
		backupFailure(w, err)
		return
	}
	write(w, 200, map[string]any{"items": items, "truncated": len(items) >= 128})
}
func (s *Server) backupJobs(w http.ResponseWriter, r *http.Request) {
	items, next, err := s.Store.BackupJobs(r.Context(), r.URL.Query().Get("cursor"), r.URL.Query().Get("destination_id"))
	if err != nil {
		backupFailure(w, err)
		return
	}
	write(w, 200, map[string]any{"items": items, "next_cursor": next})
}
func (s *Server) backupJob(w http.ResponseWriter, r *http.Request) {
	job, err := s.Store.BackupJob(r.Context(), r.PathValue("id"))
	if err != nil {
		backupFailure(w, err)
		return
	}
	write(w, 200, job)
}
func (s *Server) createBackup(w http.ResponseWriter, r *http.Request) {
	if !s.backupEncryption(w) {
		return
	}
	idem, ok := backupIdempotency(w, r)
	if !ok {
		return
	}
	var in struct {
		DestinationID string        `json:"destination_id"`
		Source        backup.Source `json:"source"`
	}
	if !decode(w, r, &in) {
		return
	}
	if err := in.Source.Validate(); err != nil {
		backupFailure(w, err)
		return
	}
	if err := s.Store.AuthorizeBackupSource(r.Context(), who(r), in.Source); err != nil {
		backupFailure(w, err)
		return
	}
	if _, err := s.Backups.Runtime.Resolve(r.Context(), in.Source); err != nil {
		problem(w, 409, "backup_target_unavailable", err.Error())
		return
	}
	job, err := s.Store.EnqueueBackup(r.Context(), who(r), backup.Job{Kind: "backup", DestinationID: in.DestinationID, Source: in.Source}, idem)
	if err != nil {
		backupFailure(w, err)
		return
	}
	write(w, 202, job)
}
func (s *Server) cancelBackup(w http.ResponseWriter, r *http.Request) {
	var in struct{}
	if !decode(w, r, &in) {
		return
	}
	job, err := s.Store.CancelBackupJob(r.Context(), who(r), r.PathValue("id"))
	if err != nil {
		backupFailure(w, err)
		return
	}
	write(w, 202, job)
}
func (s *Server) backupArtifacts(w http.ResponseWriter, r *http.Request) {
	items, next, err := s.Store.BackupArtifacts(r.Context(), r.URL.Query().Get("cursor"), r.URL.Query().Get("destination_id"))
	if err != nil {
		backupFailure(w, err)
		return
	}
	write(w, 200, map[string]any{"items": items, "next_cursor": next})
}
func (s *Server) backupArtifact(w http.ResponseWriter, r *http.Request) {
	a, err := s.Store.BackupArtifact(r.Context(), r.PathValue("id"))
	if err != nil {
		backupFailure(w, err)
		return
	}
	write(w, 200, a)
}
func (s *Server) deleteBackupArtifact(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Confirmation string `json:"confirmation"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.Confirmation != r.PathValue("id") {
		// Deliberately not "the encrypted object": a backup the database engine
		// performed is a tree of objects the server never encrypted. The check runs
		// before the artifact is loaded, so the message cannot be per-kind without
		// changing which error a wrong confirmation for a missing artifact returns.
		problem(w, 400, "confirmation_required", "Confirm the exact artifact ID to delete this backup's objects from storage.")
		return
	}
	a, err := s.Store.BackupArtifact(r.Context(), r.PathValue("id"))
	if err != nil {
		backupFailure(w, err)
		return
	}
	var active bool
	if err = s.Store.Pool.QueryRow(r.Context(), "SELECT EXISTS(SELECT 1 FROM backup_jobs WHERE artifact_id=$1 AND kind='restore' AND status IN ('queued','running'))", a.ID).Scan(&active); err != nil {
		backupFailure(w, err)
		return
	}
	if active {
		backupFailure(w, backup.ErrConflict)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	if err = s.Backups.DeleteArtifact(ctx, a); err != nil {
		backupFailure(w, err)
		return
	}
	_ = s.Store.RuntimeAudit(ctx, who(r), "backup.artifact.deleted", a.ID, map[string]any{})
	write(w, 200, map[string]bool{"deleted": true})
}

// restoreWarnings is what an operator reads before retyping a database name to
// authorize a destructive restore, so it must describe the checks this kind of
// backup actually gets. A logical dump is downloaded, counted, checksummed and
// fully authenticated before any SQL runs. A backup the database engine wrote
// for itself has none of that: the bytes never passed through this server, there
// is no digest and there is nothing to authenticate. The only independent
// evidence is the object listing taken before the artifact was recorded.
func restoreWarnings(source backup.Source) []string {
	warnings := []string{"Creates only the new named database; an existing database is refused. Application connections are not changed.", "The encrypted object checksum and complete age authentication are verified before creating the target database.", "A failed or interrupted restore may leave the new database partial. It is never automatically dropped or retried.", "Preserve the recovery key separately. Database server roles, grants and server configuration are outside this logical restore."}
	if backup.EngineManaged(source) {
		warnings = []string{
			"Restores into the new named database only. Application connections are not changed.",
			"The database engine performed this backup itself and reported it finished. Before recording it, Hakopod listed the destination prefix and confirmed that objects exist there and that their number is at least the file count the engine reported writing. That listing is the whole of the confirmation.",
			"Hakopod did not read, decrypt or checksum the contents of this backup. The bytes never passed through this server, so there is no checksum and no payload authentication to verify before the restore starts.",
			"The database engine performs the restore. A failed or interrupted restore may leave the new database partial. It is never automatically dropped or retried, and cancelling stops Hakopod from waiting without aborting work the engine has already started.",
			"Objects written by the database engine are not encrypted by Hakopod, so the destination's recovery key does not cover them. Database server users, grants, settings and other databases are outside this restore.",
		}
	}
	if source.Kind == "management" {
		warnings = append(warnings, "This restores management records into a separate database only. Reconnecting Hakopod requires an offline recovery procedure, the original authentication encryption key and matching cluster/configuration; this operation does not switch the running server.")
	}
	return warnings
}
func (s *Server) planBackupRestore(w http.ResponseWriter, r *http.Request) {
	if !s.backupEncryption(w) {
		return
	}
	var in struct {
		ApplicationID string `json:"application_id"`
		Service       string `json:"service"`
	}
	if !decode(w, r, &in) {
		return
	}
	a, err := s.Store.BackupArtifact(r.Context(), r.PathValue("id"))
	if err != nil {
		backupFailure(w, err)
		return
	}
	application, err := s.Store.Application(r.Context(), in.ApplicationID)
	if err != nil {
		failure(w, err)
		return
	}
	if err := s.Store.AuthorizeBackupSource(r.Context(), who(r), backup.Source{Kind: "database", ApplicationID: application.ID}); err != nil {
		backupFailure(w, err)
		return
	}
	service, ok := application.Spec.Services[in.Service]
	if !ok {
		backupFailure(w, backup.ErrNotFound)
		return
	}
	source, ok := declaredBackupSource(application, in.Service, service)
	if !ok || source.Engine != a.Source.Engine {
		problem(w, 400, "engine_mismatch", "Select an official database service using the same engine as the backup.")
		return
	}
	target, err := s.Backups.Runtime.Resolve(r.Context(), source)
	if err != nil {
		problem(w, 409, "restore_target_unavailable", err.Error())
		return
	}
	id := store.NewID()
	target.Database = "hp_restore_" + id[:20]
	plan := backup.RestorePlan{ID: id, ArtifactID: a.ID, Target: target, Confirmation: target.Database, Scope: a.Scope, ExpiresAt: time.Now().UTC().Add(10 * time.Minute), Warnings: restoreWarnings(a.Source)}
	if err = s.Store.SaveBackupRestorePlan(r.Context(), who(r), plan); err != nil {
		backupFailure(w, err)
		return
	}
	write(w, 200, plan)
}
func (s *Server) restoreBackup(w http.ResponseWriter, r *http.Request) {
	if !s.backupEncryption(w) {
		return
	}
	idem, ok := backupIdempotency(w, r)
	if !ok {
		return
	}
	var in struct {
		PlanID       string `json:"plan_id"`
		Confirmation string `json:"confirmation"`
	}
	if !decode(w, r, &in) {
		return
	}
	job, err := s.Store.AcceptBackupRestore(r.Context(), who(r), r.PathValue("id"), in.PlanID, in.Confirmation, idem)
	if err != nil {
		backupFailure(w, err)
		return
	}
	write(w, 202, job)
}
func (s *Server) backupSchedules(w http.ResponseWriter, r *http.Request) {
	items, err := s.Store.BackupSchedules(r.Context())
	if err != nil {
		backupFailure(w, err)
		return
	}
	write(w, 200, map[string]any{"items": items})
}
func (s *Server) putBackupSchedule(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name             string        `json:"name"`
		DestinationID    string        `json:"destination_id"`
		Source           backup.Source `json:"source"`
		IntervalHours    int           `json:"interval_hours"`
		RetentionCount   int           `json:"retention_count"`
		Enabled          bool          `json:"enabled"`
		ExpectedRevision int64         `json:"expected_revision"`
	}
	if !decode(w, r, &in) {
		return
	}
	id := r.PathValue("id")
	if r.Method == "POST" {
		if in.ExpectedRevision != 0 {
			backupFailure(w, backup.ErrConflict)
			return
		}
		id = store.NewID()
	}
	schedule := backup.Schedule{ID: id, Name: in.Name, DestinationID: in.DestinationID, Source: in.Source, IntervalHours: in.IntervalHours, RetentionCount: in.RetentionCount, Enabled: in.Enabled}
	if err := schedule.Validate(); err != nil {
		backupFailure(w, err)
		return
	}
	if err := s.Store.AuthorizeBackupSource(r.Context(), who(r), in.Source); err != nil {
		backupFailure(w, err)
		return
	}
	if _, err := s.Backups.Runtime.Resolve(r.Context(), in.Source); err != nil {
		problem(w, 409, "backup_target_unavailable", err.Error())
		return
	}
	schedule, err := s.Store.PutBackupSchedule(r.Context(), who(r), schedule, in.ExpectedRevision)
	if err != nil {
		backupFailure(w, err)
		return
	}
	status := 200
	if r.Method == "POST" {
		status = 201
	}
	write(w, status, schedule)
}
func (s *Server) deleteBackupSchedule(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ExpectedRevision int64 `json:"expected_revision"`
	}
	if !decode(w, r, &in) {
		return
	}
	if err := s.Store.DeleteBackupSchedule(r.Context(), who(r), r.PathValue("id"), in.ExpectedRevision); err != nil {
		backupFailure(w, err)
		return
	}
	write(w, 200, map[string]bool{"deleted": true})
}
