package database

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"
)

const InfisicalKnexPostgresProfile = "infisical-knex-postgresql-v1"

type MigrationLockEvidence struct {
	SchemaVersion          int       `json:"schema_version"`
	Profile                string    `json:"profile"`
	DatabaseID             string    `json:"database_id"`
	DatabaseRevision       int64     `json:"database_revision"`
	ApplicationID          string    `json:"application_id"`
	ApplicationRevision    int64     `json:"application_revision"`
	Service                string    `json:"service"`
	Variable               string    `json:"variable"`
	LogicalDatabase        string    `json:"logical_database"`
	ApplicationImage       string    `json:"application_image"`
	SchemaFingerprint      string    `json:"schema_fingerprint"`
	KnexLockTable          string    `json:"knex_lock_table"`
	KnexLockRows           int       `json:"knex_lock_rows"`
	KnexLockedRows         int       `json:"knex_locked_rows"`
	StartupLockRows        int       `json:"startup_lock_rows"`
	StartupLockedRows      int       `json:"startup_locked_rows"`
	FreshStartupHeartbeats int       `json:"fresh_startup_heartbeats"`
	ActiveMigratorSessions int       `json:"active_migrator_sessions"`
	ActiveApplicationPods  int       `json:"active_application_pods"`
	ActiveMigrationJobs    int       `json:"active_migration_jobs"`
	ActiveMigrationLocks   int       `json:"active_migration_locks"`
	ObservedAt             time.Time `json:"observed_at"`
}

func (e MigrationLockEvidence) Digest() string {
	b, _ := json.Marshal(e)
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func (e MigrationLockEvidence) Repairable() bool {
	return e.SchemaVersion == 1 && e.Profile == InfisicalKnexPostgresProfile && e.ApplicationImage == "docker.io/infisical/infisical:v0.165.10@sha256:204bd63c7a281d9157752ce0bf8d506e7380cac5a0665324eeab8d580b069266" && e.SchemaFingerprint == "infisical-knex-postgresql-v1:lock(index:int,is_locked:int):startup(index:int,is_locked:int,session_id:text,node:text,heartbeat:timestamptz)" && e.KnexLockRows == 1 && e.KnexLockedRows == 1 && e.FreshStartupHeartbeats == 0 && e.ActiveMigratorSessions == 0 && e.ActiveApplicationPods == 0 && e.ActiveMigrationJobs == 0 && e.ActiveMigrationLocks == 0
}

func (e MigrationLockEvidence) SameState(other MigrationLockEvidence) bool {
	e.ObservedAt, other.ObservedAt = time.Time{}, time.Time{}
	return e == other
}

type MigrationLockRecoveryPlan struct {
	ID              string                `json:"id"`
	Evidence        MigrationLockEvidence `json:"evidence"`
	EvidenceSHA256  string                `json:"evidence_sha256"`
	ApplicationName string                `json:"application_name"`
	DatabaseName    string                `json:"database_name"`
	Warnings        []string              `json:"warnings"`
	ExpiresAt       time.Time             `json:"expires_at"`
}

type MigrationLockRecoveryOperation struct {
	ID         string                    `json:"id"`
	DatabaseID string                    `json:"database_id"`
	Status     string                    `json:"status"`
	Phase      string                    `json:"phase"`
	Message    string                    `json:"message"`
	Plan       MigrationLockRecoveryPlan `json:"plan"`
	Before     MigrationLockEvidence     `json:"before"`
	After      *MigrationLockEvidence    `json:"after,omitempty"`
	CreatedAt  time.Time                 `json:"created_at"`
	StartedAt  *time.Time                `json:"started_at,omitempty"`
	FinishedAt *time.Time                `json:"finished_at,omitempty"`
	IdentityID string                    `json:"-"`
	KeyID      string                    `json:"-"`
	Lease      string                    `json:"-"`
}
