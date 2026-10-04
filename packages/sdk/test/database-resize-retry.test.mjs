import assert from "node:assert/strict";
import test from "node:test";
import { Hakopod, db } from "../dist/index.js";

// Artificial API records test the client contract, not a database runtime.
const database = { id: "a".repeat(32), project: "fixture", environment: "development", revision: 2,
  spec: db({ name: "orders", engine: "mysql", version: "8.4", mode: "cluster", replicas: 4, cpu: "500m", memory: "1Gi", storageGiB: 1 }), status: "failed" };
const source = "b".repeat(32);
const review = { id: "c".repeat(32), plan: { operation_id: source, database_id: database.id, revision: 2, state: "prior", expires_at: "2099-01-01T00:00:00Z",
  resize: { current: { ...database.spec, replicas: 2 }, proposed: database.spec, expected_revision: 1, topology_fingerprint: "fixture", blocked_reasons: [], warnings: [], expires_at: "2099-01-01T00:00:00Z" } } };
const operation = { id: "d".repeat(32), database_id: database.id, revision: 2, kind: "resize-retry", status: "succeeded", phase: "ready", message: "", spec: database.spec, created_at: "2098-12-31T23:50:00Z" };
function fixture(change = {}) {
  const calls = [];
  const client = new Hakopod({ apiUrl: "https://fixture.invalid/api/v1", apiKey: "hp_fixture", project: "fixture", environment: "development", maxRetries: 0,
    fetch: async (url, init) => {
      const path = new URL(url).pathname.slice("/api/v1".length);
      calls.push({ path, method: init.method, body: init.body && JSON.parse(init.body), key: init.headers.get("Idempotency-Key") });
      return Response.json(path.endsWith("/resize-retry-plan") ? (change.review || review) : path.endsWith("/resize-retry") || path.includes("database-operations") ? (change.operation || operation) : (change.database || database));
    } });
  return { ref: client.database(database.id), calls };
}

test("replica retry preserves the operation, reviewed revision and request key", async () => {
  const { ref, calls } = fixture();
  const plan = await ref.resizeRetryPlan(source);
  assert.deepEqual(calls.at(-1).body, { operation_id: source, expected_revision: 2 });
  assert(Object.isFrozen(plan.plan.resize.proposed));
  const run = await ref.retryResize({ operationId: source, reviewId: plan.id, expectedRevision: plan.plan.revision, confirmName: "orders" }, { idempotencyKey: "stable-replica-retry" });
  assert.deepEqual(calls.at(-1), { path: `/databases/${database.id}/resize-retry`, method: "POST", key: "stable-replica-retry",
    body: { operation_id: source, review_id: plan.id, expected_revision: 2, confirm_name: "orders" } });
  assert.equal(run.idempotencyKey, "stable-replica-retry");
  assert.equal((await run.wait()).id, operation.id);
});

test("replica retry requires confirmation and the reviewed current revision", async () => {
  for (const [input, code] of [
    [{ confirmName: "other" }, "confirmation_required"], [{ expectedRevision: 1 }, "revision_conflict"], [{ expectedRevision: 0 }, "invalid_option"],
  ]) {
    const { ref, calls } = fixture();
    await assert.rejects(ref.retryResize({ operationId: source, reviewId: review.id, expectedRevision: 2, confirmName: "orders", ...input }), { code });
    assert(calls.every((call) => call.method === "GET"));
  }
  const { ref, calls } = fixture({ database: { ...database, spec: { ...database.spec, mode: "standalone", replicas: 0 } } });
  await assert.rejects(ref.resizeRetryPlan(source), { code: "invalid_option" });
  assert(calls.every((call) => call.method === "GET"));
});

test("replica retry rejects a mismatched review or returned operation", async () => {
  for (const change of [{ database_id: "other" }, { operation_id: "other" }, { state: "unknown" }, { revision: 3 }]) {
    const { ref } = fixture({ review: { ...review, plan: { ...review.plan, ...change } } });
    await assert.rejects(ref.resizeRetryPlan(source));
  }
  for (const change of [{ database_id: "other" }, { revision: 3 }, { kind: "resize" }, { id: source }]) {
    const { ref } = fixture({ operation: { ...operation, ...change } });
    await assert.rejects(ref.retryResize({ operationId: source, reviewId: review.id, expectedRevision: 2, confirmName: "orders" }), { code: "invalid_response" });
  }
});
