import assert from "node:assert/strict";
import test from "node:test";
import { Hakopod, db } from "../dist/index.js";

const database = { id: "a".repeat(32), project: "fixture", environment: "development", revision: 4, spec: db({ name: "postgres", engine: "postgresql", version: "17", mode: "standalone", replicas: 0, cpu: "500m", memory: "1Gi", storageGiB: 1 }), status: "ready" };
const evidence = { schema_version: 1, profile: "infisical-knex-postgresql-v1", database_id: database.id, database_revision: 4, application_id: "b".repeat(32), application_revision: 3, service: "main", variable: "DB_CONNECTION_URI", logical_database: "infisical", application_image: "docker.io/infisical/infisical:v0.165.10@sha256:" + "c".repeat(64), schema_fingerprint: "fingerprint", knex_lock_table: "infisical_migrations_lock", knex_lock_rows: 1, knex_locked_rows: 1, startup_lock_rows: 1, startup_locked_rows: 0, fresh_startup_heartbeats: 0, active_migrator_sessions: 0, active_application_pods: 0, active_migration_jobs: 0, active_migration_locks: 0, observed_at: "2099-01-01T00:00:00Z" };
const provisioningPlan = { schema_version: 1, id: "d".repeat(32), database_id: database.id, database_revision: 4, application_id: evidence.application_id, application_name: "infisical", application_revision: 3, service: "main", variable: "DB_CONNECTION_URI", endpoint: "read_write", role: "hp_infisical", logical_database: "infisical", secret_reference: "database-password", privileges: ["CONNECT"], warnings: [], expires_at: "2099-01-01T00:05:00Z" };
const migrationPlan = { id: "e".repeat(32), evidence, evidence_sha256: "f".repeat(64), application_name: "infisical", database_name: "postgres", warnings: [], expires_at: "2099-01-01T00:05:00Z" };

function fixture() {
  const calls = [];
  const client = new Hakopod({ apiUrl: "https://fixture.invalid/api/v1", apiKey: "hp_fixture", project: "fixture", environment: "development", maxRetries: 0, fetch: async (url, init) => {
    const path = new URL(url).pathname.slice("/api/v1".length); const body = init.body && JSON.parse(init.body); calls.push({ path, method: init.method, body, key: init.headers.get("Idempotency-Key") });
    if (path.endsWith("application-provisioning-plan")) return Response.json(provisioningPlan);
    if (path.endsWith("application-provision")) return Response.json({ id: "1".repeat(32), database_id: database.id, database_revision: 4, kind: "application-provisioning", status: "queued", phase: "accepted", message: "", plan: provisioningPlan, created_at: "2099-01-01T00:00:00Z" }, { status: 202 });
    if (path.endsWith("migration-lock-recovery-plan")) return Response.json(migrationPlan);
    if (path.endsWith("migration-lock-recover")) return Response.json({ id: "2".repeat(32), database_id: database.id, status: "queued", phase: "accepted", message: "", plan: migrationPlan, before: evidence, created_at: "2099-01-01T00:00:00Z" }, { status: 202 });
    return Response.json(database);
  }});
  return { ref: client.database(database.id), calls };
}

test("application provisioning helper preserves reviewed path, body and idempotency key", async () => {
  const { ref, calls } = fixture();
  const review = await ref.applicationProvisioningPlan({ applicationId: evidence.application_id, service: "main", variable: "DB_CONNECTION_URI", role: "hp_infisical", database: "infisical", secretReference: "database-password" });
  assert.deepEqual(calls.at(-1).body, { application_id: evidence.application_id, service: "main", variable: "DB_CONNECTION_URI", role: "hp_infisical", database: "infisical", secret_reference: "database-password" });
  const run = await review.apply({ idempotencyKey: "provision-once" });
  assert.deepEqual(calls.at(-1), { path: `/databases/${database.id}/application-provision`, method: "POST", key: "provision-once", body: { review_id: provisioningPlan.id, confirm_application: "infisical" } });
  assert.equal(run.databaseId, database.id);
});

test("migration recovery helper pins the supported profile and both confirmations", async () => {
  const { ref, calls } = fixture();
  const review = await ref.migrationLockRecoveryPlan({ applicationId: evidence.application_id, service: "main", variable: "DB_CONNECTION_URI" });
  assert.deepEqual(calls.at(-1).body, { application_id: evidence.application_id, service: "main", variable: "DB_CONNECTION_URI", profile: "infisical-knex-postgresql-v1" });
  const run = await review.apply({ idempotencyKey: "repair-once" });
  assert.deepEqual(calls.at(-1), { path: `/databases/${database.id}/migration-lock-recover`, method: "POST", key: "repair-once", body: { review_id: migrationPlan.id, confirm_application: "infisical", confirm_database: "postgres" } });
  assert.equal(run.databaseId, database.id);
});
