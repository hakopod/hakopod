package database

import (
	"testing"
	"time"
)

func TestInfisicalMigrationEvidenceRequiresPositiveAbandonmentEvidence(t *testing.T) {
	base := MigrationLockEvidence{SchemaVersion: 1, Profile: InfisicalKnexPostgresProfile, DatabaseID: "database", DatabaseRevision: 1, ApplicationID: "application", ApplicationRevision: 2, Service: "main", Variable: "DB_CONNECTION_URI", LogicalDatabase: "infisical", ApplicationImage: "docker.io/infisical/infisical:v0.165.10@sha256:204bd63c7a281d9157752ce0bf8d506e7380cac5a0665324eeab8d580b069266", SchemaFingerprint: "infisical-knex-postgresql-v1:lock(index:int,is_locked:int):startup(index:int,is_locked:int,session_id:text,node:text,heartbeat:timestamptz)", KnexLockTable: "infisical_migrations_lock", KnexLockRows: 1, KnexLockedRows: 1, ObservedAt: time.Now()}
	if !base.Repairable() {
		t.Fatal("complete abandoned-lock evidence was refused")
	}
	for name, mutate := range map[string]func(*MigrationLockEvidence){"active session": func(e *MigrationLockEvidence) { e.ActiveMigratorSessions = 1 }, "active pod": func(e *MigrationLockEvidence) { e.ActiveApplicationPods = 1 }, "active job": func(e *MigrationLockEvidence) { e.ActiveMigrationJobs = 1 }, "active table lock": func(e *MigrationLockEvidence) { e.ActiveMigrationLocks = 1 }, "heartbeat": func(e *MigrationLockEvidence) { e.FreshStartupHeartbeats = 1 }, "unlocked": func(e *MigrationLockEvidence) { e.KnexLockedRows = 0 }, "extra row": func(e *MigrationLockEvidence) { e.KnexLockRows = 2 }, "unsupported profile": func(e *MigrationLockEvidence) { e.Profile = "django" }, "unpinned image": func(e *MigrationLockEvidence) { e.ApplicationImage = "infisical:latest" }, "schema drift": func(e *MigrationLockEvidence) { e.SchemaFingerprint = "other" }} {
		t.Run(name, func(t *testing.T) {
			e := base
			mutate(&e)
			if e.Repairable() {
				t.Fatal("unsafe evidence was repairable")
			}
		})
	}
	later := base
	later.ObservedAt = base.ObservedAt.Add(time.Second)
	if !base.SameState(later) {
		t.Fatal("observation timestamp changed otherwise identical state")
	}
	later.KnexLockedRows = 0
	if base.SameState(later) {
		t.Fatal("lock state change was ignored")
	}
}
