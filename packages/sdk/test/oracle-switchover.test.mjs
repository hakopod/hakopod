import assert from "node:assert/strict";
import test from "node:test";
import { Hakopod, db } from "../dist/index.js";

// Synthetic API records only. These tests do not attest an Oracle runtime.
const database = {
  id: "a".repeat(32), project: "sdk-fixture", environment: "development", revision: 4,
  spec: db({ name: "orders", engine: "oracle", mode: "cluster", replicas: 2, cpu: "1", memory: "4Gi", storageGiB: 20,
    oracle: { edition: "enterprise", image: "fixture.invalid/oracle@sha256:" + "b".repeat(64), license_confirmed: true } }),
  status: "ready",
};
const plan = {
  id: "c".repeat(32), warnings: ["Existing connections will close."],
  plan: { request_id: "d".repeat(32), database_id: database.id, project: database.project, environment: database.environment,
    revision: database.revision, broker_uid: "fixture-broker", topology_fingerprint: "fixture-topology", primary: "primary-pod",
    target: "standby-pod", target_controller: "database-1", target_unique_name: "HPDB1",
    member_uids: ["primary", "standby-1", "standby-2"], expires_at: "2099-01-01T00:00:00Z" },
};
const operation = {
  id: "e".repeat(32), database_id: database.id, revision: database.revision, kind: "switchover",
  status: "succeeded", phase: "ready", message: "", spec: database.spec, switchover: plan.plan,
  created_at: "2098-12-31T23:50:00Z",
};
function fixture(handler = (call) => Response.json(
  call.path.endsWith("/switchover-plan") ? plan :
  call.path.includes("switchover") || call.path.includes("database-operations") ? operation : database,
)) {
  const calls = [];
  const client = new Hakopod({ apiUrl: "https://fixture.invalid/api/v1", apiKey: "hp_fixture",
    project: database.project, environment: database.environment, maxRetries: 0,
    fetch: async (url, init) => {
      const call = { path: new URL(url).pathname.slice("/api/v1".length), method: init.method,
        body: init.body && JSON.parse(init.body), key: init.headers.get("Idempotency-Key") };
      calls.push(call);
      return handler(call);
    },
  });
  return { ref: client.database(database.id), calls };
}

test("Oracle switchover keeps the reviewed identity and retry key and returns a waitable operation", async () => {
  const { ref, calls } = fixture();
  const review = await ref.switchoverPlan("standby-pod");
  assert.deepEqual(calls.at(-1).body, { target_member: "standby-pod" });
  assert(Object.isFrozen(review.plan.member_uids));
  assert.throws(() => { review.plan.target = "another-pod"; }, TypeError);
  const run = await ref.switchover({ reviewId: review.id, expectedRevision: review.plan.revision, confirmName: "orders" }, { idempotencyKey: "stable-oracle-retry-key" });
  assert.equal(run.idempotencyKey, "stable-oracle-retry-key");
  assert.deepEqual(calls.at(-1), { path: `/databases/${database.id}/switchover`, method: "POST", key: "stable-oracle-retry-key",
    body: { review_id: plan.id, expected_revision: 4, confirm_name: "orders" } });
  assert.deepEqual((await run.wait()).switchover, plan.plan);
});

test("Oracle switchover requires explicit name confirmation and a current positive revision", async () => {
  const { ref, calls } = fixture();
  for (const [input, code] of [
    [{ reviewId: plan.id, expectedRevision: 4, confirmName: "other" }, "confirmation_required"],
    [{ reviewId: plan.id, expectedRevision: 3, confirmName: "orders" }, "revision_conflict"],
    [{ reviewId: plan.id, expectedRevision: 0, confirmName: "orders" }, "invalid_option"],
  ]) await assert.rejects(ref.switchover(input), { code });
  assert(calls.every((call) => call.method === "GET"));
  const unsupported = fixture(() => Response.json({ ...database, spec: { ...database.spec, oracle: { edition: "free" } } }));
  await assert.rejects(unsupported.ref.switchoverPlan("standby-pod"), { code: "invalid_option" });
  assert(unsupported.calls.every((call) => call.method === "GET"));
});

test("Oracle switchover rejects changed plan scope, target and revision", async () => {
  for (const [change, code] of [
    [{ database_id: "other" }, "invalid_response"], [{ project: "other" }, "invalid_response"],
    [{ environment: "production" }, "invalid_response"], [{ target: "other" }, "invalid_response"],
    [{ revision: 5 }, "revision_conflict"],
  ]) {
    const { ref } = fixture((call) => Response.json(call.path.endsWith("/switchover-plan") ? { ...plan, plan: { ...plan.plan, ...change } } : database));
    await assert.rejects(ref.switchoverPlan("standby-pod"), { code });
  }
});

test("Oracle retry resumes the exact operation without a new review or target", async () => {
  const { ref, calls } = fixture();
  const run = await ref.retrySwitchover({ operationId: operation.id, expectedRevision: 4, confirmName: "orders" });
  assert.equal(run.id, operation.id);
  assert.deepEqual(calls.at(-1), { path: `/databases/${database.id}/switchover-retry`, method: "POST", key: null,
    body: { operation_id: operation.id, expected_revision: 4, confirm_name: "orders" } });
  for (const change of [{ id: "other-operation" }, { database_id: "other" }, { kind: "resize" }, { switchover: undefined }]) {
    const wrong = fixture((call) => Response.json(call.path.endsWith("/switchover-retry") ? { ...operation, ...change } : database));
    await assert.rejects(wrong.ref.retrySwitchover({ operationId: operation.id, expectedRevision: 4, confirmName: "orders" }), { code: "invalid_response" });
  }
});
