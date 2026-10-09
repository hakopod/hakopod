package cluster

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/hakopod/hakopod/internal/database"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const infisicalMigrationInspectionSQL = `
SELECT
 (SELECT count(*) FROM infisical_migrations_lock),
 (SELECT count(*) FROM infisical_migrations_lock WHERE is_locked=1),
 (SELECT count(*) FROM infisical_migrations_startup_lock),
 (SELECT count(*) FROM infisical_migrations_startup_lock WHERE is_locked=1),
 (SELECT count(*) FROM infisical_migrations_startup_lock WHERE is_locked=1 AND heartbeat_updated_at >= now()-interval '15 seconds'),
 (SELECT count(*) FROM pg_stat_activity WHERE pid<>pg_backend_pid() AND datname=current_database() AND state<>'idle'),
 (SELECT count(*) FROM pg_locks l JOIN pg_class c ON c.oid=l.relation WHERE l.pid<>pg_backend_pid() AND c.relname IN ('infisical_migrations_lock','infisical_migrations_startup_lock'));`

const infisicalMigrationSchemaSQL = `SELECT string_agg(table_name||':'||column_name||':'||data_type,',' ORDER BY table_name,ordinal_position) FROM information_schema.columns WHERE table_schema=current_schema() AND table_name IN ('infisical_migrations_lock','infisical_migrations_startup_lock')`
const infisicalMigrationSchemaFingerprint = "infisical_migrations_lock:index:integer,infisical_migrations_lock:is_locked:integer,infisical_migrations_startup_lock:index:integer,infisical_migrations_startup_lock:is_locked:integer,infisical_migrations_startup_lock:session_id:character varying,infisical_migrations_startup_lock:node:character varying,infisical_migrations_startup_lock:heartbeat_updated_at:timestamp with time zone"

func (c *Client) InspectInfisicalMigrationLock(ctx context.Context, d database.Resource, applicationID string, applicationRevision int64, service, variable, logicalDatabase, image string) (database.MigrationLockEvidence, error) {
	e := database.MigrationLockEvidence{SchemaVersion: 1, Profile: database.InfisicalKnexPostgresProfile, DatabaseID: d.ID, DatabaseRevision: d.Revision, ApplicationID: applicationID, ApplicationRevision: applicationRevision, Service: service, Variable: variable, LogicalDatabase: logicalDatabase, ApplicationImage: image, KnexLockTable: "infisical_migrations_lock", ObservedAt: time.Now().UTC()}
	if d.Spec.Engine != "postgresql" || d.Status != "ready" || applicationID == "" || applicationRevision < 1 || service == "" || variable == "" || !nativeProvisionIdentifier.MatchString(logicalDatabase) {
		return e, fmt.Errorf("Infisical migration-lock inspection requires a ready PostgreSQL database and exact application revision")
	}
	if len(d.Observation.Members) == 0 {
		return e, fmt.Errorf("PostgreSQL primary observation is unavailable")
	}
	member := d.Observation.Members[0]
	for _, m := range d.Observation.Members {
		if m.Role == "primary" {
			member = m
		}
	}
	out := &databaseBoundedWriter{limit: 4096}
	schema := &databaseBoundedWriter{limit: 4096}
	if err := c.DatabaseExec(ctx, d, member, []string{"psql", "-XAt", "-v", "ON_ERROR_STOP=1", "-U", "postgres", "-d", logicalDatabase, "-c", infisicalMigrationSchemaSQL}, nil, schema); err != nil || strings.TrimSpace(schema.String()) != infisicalMigrationSchemaFingerprint {
		return e, fmt.Errorf("Infisical migration lock schema does not match the pinned profile")
	}
	e.SchemaFingerprint = "infisical-knex-postgresql-v1:lock(index:int,is_locked:int):startup(index:int,is_locked:int,session_id:text,node:text,heartbeat:timestamptz)"
	if err := c.DatabaseExec(ctx, d, member, []string{"psql", "-XAt", "-F", "|", "-v", "ON_ERROR_STOP=1", "-U", "postgres", "-d", logicalDatabase, "-c", infisicalMigrationInspectionSQL}, nil, out); err != nil {
		return e, fmt.Errorf("Infisical migration lock tables do not match the supported profile")
	}
	parts := strings.Split(strings.TrimSpace(out.String()), "|")
	if len(parts) != 7 {
		return e, fmt.Errorf("Infisical migration lock evidence is incomplete")
	}
	values := make([]int, 7)
	for i, v := range parts {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 || n > 10000 {
			return e, fmt.Errorf("Infisical migration lock evidence is invalid")
		}
		values[i] = n
	}
	e.KnexLockRows, e.KnexLockedRows, e.StartupLockRows, e.StartupLockedRows, e.FreshStartupHeartbeats, e.ActiveMigratorSessions = values[0], values[1], values[2], values[3], values[4], values[5]
	e.ActiveMigrationLocks = values[6]
	pods, err := c.kube.CoreV1().Pods(Namespace(applicationID)).List(ctx, metav1.ListOptions{Limit: 101})
	if err != nil || pods.Continue != "" || len(pods.Items) > 100 {
		return e, fmt.Errorf("application runtime state is unavailable")
	}
	for _, pod := range pods.Items {
		if pod.DeletionTimestamp != nil || pod.Status.Phase != "Succeeded" && pod.Status.Phase != "Failed" {
			e.ActiveApplicationPods++
		}
	}
	jobs, err := c.kube.BatchV1().Jobs(Namespace(applicationID)).List(ctx, metav1.ListOptions{Limit: 101})
	if err != nil || jobs.Continue != "" || len(jobs.Items) > 100 {
		return e, fmt.Errorf("application migration job state is unavailable")
	}
	for _, job := range jobs.Items {
		if job.DeletionTimestamp != nil || job.Status.Active > 0 {
			e.ActiveMigrationJobs++
		}
	}
	return e, nil
}

func (c *Client) RepairInfisicalMigrationLock(ctx context.Context, d database.Resource, reviewed database.MigrationLockEvidence, before func() error) (database.MigrationLockEvidence, error) {
	if !reviewed.Repairable() || reviewed.DatabaseID != d.ID || reviewed.DatabaseRevision != d.Revision || time.Since(reviewed.ObservedAt) > database.ReviewLifetime {
		return database.MigrationLockEvidence{}, fmt.Errorf("reviewed migration evidence is not repairable")
	}
	if before != nil {
		if err := before(); err != nil {
			return database.MigrationLockEvidence{}, err
		}
	}
	fresh, err := c.InspectInfisicalMigrationLock(ctx, d, reviewed.ApplicationID, reviewed.ApplicationRevision, reviewed.Service, reviewed.Variable, reviewed.LogicalDatabase, reviewed.ApplicationImage)
	if err != nil {
		return fresh, err
	}
	if !fresh.SameState(reviewed) || !fresh.Repairable() {
		return fresh, fmt.Errorf("migration lock evidence changed; inspect and review again")
	}
	member := d.Observation.Members[0]
	for _, m := range d.Observation.Members {
		if m.Role == "primary" {
			member = m
		}
	}
	if before != nil {
		if err = before(); err != nil {
			return fresh, err
		}
	}
	// This exactly matches Knex 3.0.1 forceFreeMigrationsLock: delete the
	// existing rows, then insert one unlocked row. Both statements are one
	// transaction and the immediately preceding evidence is still fenced.
	repair := `BEGIN; SET LOCAL lock_timeout='3s'; SET LOCAL statement_timeout='10s'; LOCK TABLE infisical_migrations_lock,infisical_migrations_startup_lock IN ACCESS EXCLUSIVE MODE; DO $b$ BEGIN IF (SELECT count(*) FROM infisical_migrations_lock)<>1 OR (SELECT count(*) FROM infisical_migrations_lock WHERE is_locked=1)<>1 OR EXISTS(SELECT 1 FROM infisical_migrations_startup_lock WHERE is_locked=1 AND heartbeat_updated_at>=now()-interval '15 seconds') OR EXISTS(SELECT 1 FROM pg_stat_activity WHERE pid<>pg_backend_pid() AND datname=current_database() AND state<>'idle') OR EXISTS(SELECT 1 FROM pg_locks l JOIN pg_class c ON c.oid=l.relation WHERE l.pid<>pg_backend_pid() AND c.relname IN ('infisical_migrations_lock','infisical_migrations_startup_lock')) THEN RAISE EXCEPTION 'migration evidence changed'; END IF; END $b$; DELETE FROM infisical_migrations_lock; INSERT INTO infisical_migrations_lock(is_locked) VALUES(0); COMMIT;`
	if err = c.DatabaseExec(ctx, d, member, []string{"psql", "-Xq", "-v", "ON_ERROR_STOP=1", "-U", "postgres", "-d", reviewed.LogicalDatabase, "-c", repair}, nil, io.Discard); err != nil {
		return fresh, fmt.Errorf("Knex migration lock repair did not complete")
	}
	after, err := c.InspectInfisicalMigrationLock(ctx, d, reviewed.ApplicationID, reviewed.ApplicationRevision, reviewed.Service, reviewed.Variable, reviewed.LogicalDatabase, reviewed.ApplicationImage)
	if err != nil {
		return after, err
	}
	if after.KnexLockRows != 1 || after.KnexLockedRows != 0 {
		return after, fmt.Errorf("Knex migration lock repair could not be verified")
	}
	return after, nil
}
