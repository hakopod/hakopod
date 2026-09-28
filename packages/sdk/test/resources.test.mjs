import assert from "node:assert/strict";
import test from "node:test";
import {
  Hakopod,
  app,
  service,
  db,
  network,
  APIError,
  OperationError,
  WaitError,
} from "../dist/index.js";

// Explicit synthetic API fixtures; these do not represent a running cluster.
const scope = { project: "sdk-fixture", environment: "development" };
const spec = app({
  name: "fixture-app",
  services: {
    web: service({
      image: "fixture.invalid/web@sha256:" + "a".repeat(64),
      port: 8080,
    }),
    worker: service({
      image: "fixture.invalid/worker@sha256:" + "b".repeat(64),
      replicas: 1,
    }),
  },
});
const application = {
  id: "fixture-app-id",
  name: spec.name,
  ...scope,
  revision: 3,
  spec,
  status: "healthy",
};
const plan = {
  application_id: application.id,
  expected_revision: 3,
  spec,
  changes: [],
  warnings: [],
  missing_secrets: [],
};
const database = {
  id: "fixture-db-id",
  ...scope,
  revision: 1,
  spec: db({
    name: "fixture-db",
    engine: "postgresql",
    cpu: "100m",
    memory: "256Mi",
    storageGiB: 1,
  }),
  status: "ready",
};
function fixture(handler) {
  const calls = [];
  const client = new Hakopod({
    apiUrl: "https://fixture.invalid/api/v1",
    apiKey: "hp_fixture",
    ...scope,
    maxRetries: 0,
    fetch: async (url, init) => {
      const call = {
        path: new URL(url).pathname.slice("/api/v1".length),
        method: init.method,
        body: init.body && JSON.parse(init.body),
        key: init.headers.get("Idempotency-Key"),
        url: new URL(url),
      };
      calls.push(call);
      return handler(call, calls);
    },
  });
  return { client, calls };
}
test("connect uses the machine key scope and requires an explicit scope for installation keys", async () => {
  const options = {
    apiUrl: "https://fixture.invalid",
    apiKey: "hp_fixture",
    fetch: async () => Response.json(scope),
  };
  const client = await Hakopod.connect(options);
  assert(client.app("fixture") instanceof Object);
  await assert.rejects(
    Hakopod.connect({
      ...options,
      fetch: async () => Response.json({ project: "", environment: "" }),
    }),
    { code: "missing_scope" },
  );
});
test("application deletion includes the explicitly reviewed revision", async () => {
  const { client, calls } = fixture((call) =>
    Response.json(
      call.method === "DELETE" ? { status: "deleted" } : application,
    ),
  );
  await client
    .application(application.id)
    .delete({
      confirmName: application.name,
      expectedRevision: application.revision,
    });
  assert.deepEqual(calls.at(-1).body, {
    confirm_name: application.name,
    expected_revision: application.revision,
  });
});
test("database backups and schedules use the managed database identity", async () => {
  const { client, calls } = fixture((call) =>
    Response.json(
      call.path.startsWith("/databases/") ? database : { id: "backup-fixture" },
    ),
  );
  const run = await client
    .database(database.id)
    .backup("destination-fixture", { idempotencyKey: "backup-fixture-key" });
  assert.equal(run.id, "backup-fixture");
  assert.deepEqual(calls.at(-1).body, {
    destination_id: "destination-fixture",
    source: {
      kind: "managed_database",
      engine: "postgresql",
      managed_database_id: database.id,
    },
  });
  assert.equal(calls.at(-1).key, "backup-fixture-key");
  await client
    .database(database.id)
    .schedule({
      name: "daily-fixture",
      destinationId: "destination-fixture",
      frequency: "daily",
      retentionCount: 7,
    });
  assert.equal(calls.at(-1).body.interval_hours, 24);
});
test("recovery review binds the archive and target and inspection requires explicit attestation", async () => {
  const recoveryPlan = {
    id: "recovery-fixture",
    artifact_id: "archive-fixture",
    target: { managed_database_id: database.id },
    confirmation: "restore fixture",
  };
  const { client, calls } = fixture((call) =>
    Response.json(
      call.path.endsWith("restore-plan")
        ? recoveryPlan
        : call.path.endsWith("/restore")
          ? { id: "restore-fixture" }
          : database,
    ),
  );
  const review = await client
    .database(database.id)
    .restorePlan("archive-fixture");
  assert.equal(calls.length, 2);
  await review.apply();
  assert.deepEqual(calls.at(-1).body, {
    plan_id: "recovery-fixture",
    confirmation: "restore fixture",
  });
  await assert.rejects(
    client
      .database(database.id)
      .inspectRecovery({
        jobId: "restore-fixture",
        confirmName: "wrong",
        expectedRevision: 1,
        inspected: true,
      }),
    { code: "confirmation_required" },
  );
  const wrong = fixture((call) =>
    Response.json(
      call.path.endsWith("restore-plan")
        ? { ...recoveryPlan, target: { managed_database_id: "another-db" } }
        : database,
    ),
  );
  await assert.rejects(
    wrong.client.database(database.id).restorePlan("archive-fixture"),
    { code: "invalid_response" },
  );
});
test("declarative helpers perform no I/O, set schema version, and freeze their input snapshot", () => {
  const input = { image: "fixture.invalid/image", env: { EXAMPLE: "fixture" } };
  const copy = service(input);
  input.env.EXAMPLE = "changed";
  assert.equal(copy.env.EXAMPLE, "fixture");
  assert.throws(() => {
    copy.env.EXAMPLE = "mutated";
  });
  assert.equal(app({ name: "fixture", services: {} }).schema_version, 1);
  assert.equal(network({ name: "fixture", segments: {} }).schema_version, 1);
  assert.equal(database.spec.version, "18");
  assert.deepEqual([database.spec.replicas, database.spec.shards], [0, 1]);
  const redis = db({
    name: "fixture",
    engine: "redis",
    mode: "cluster",
    cpu: "100m",
    memory: "256Mi",
    storageGiB: 1,
  });
  assert.deepEqual([redis.version, redis.replicas, redis.shards], ["8", 1, 3]);
});
test("review submits the exact snapshot and preserves selectors and idempotency across retries", async () => {
  const { client, calls } = fixture((call) =>
    Response.json(
      call.path === "/plan"
        ? plan
        : { id: "fixture-deploy", application_id: application.id },
    ),
  );
  const selected = ["web"];
  const review = await client.plan(spec, { services: selected });
  selected[0] = "worker";
  assert.equal(calls.length, 1);
  assert.throws(() => {
    review.plan.spec.services.web.image = "changed";
  });
  await review.apply();
  await review.apply();
  assert.equal(calls[1].key, calls[2].key);
  assert.equal(calls[1].key, review.idempotencyKey);
  assert.deepEqual(calls[1].body, {
    ...scope,
    spec,
    expected_revision: 3,
    services: ["web"],
  });
});
test("missing secrets, declined review and stale service edits cannot reach deployment submission", async () => {
  const { client, calls } = fixture((call) =>
    Response.json(
      call.path === "/plan"
        ? { ...plan, missing_secrets: ["fixture-secret"] }
        : application,
    ),
  );
  const review = await client.plan(spec);
  await assert.rejects(review.apply(), { code: "missing_secrets" });
  assert.equal(calls.length, 1);
  await assert.rejects(
    client
      .app("fixture-app")
      .deploy({ services: spec.services }, { review: () => false }),
    { code: "review_declined" },
  );
  assert(calls.every((call) => call.path !== "/deployments"));
  const stale = fixture((call) =>
    Response.json(
      call.path === "/plan" ? { ...plan, expected_revision: 4 } : application,
    ),
  );
  await assert.rejects(
    stale.client
      .application(application.id)
      .service("web")
      .plan({ replicas: 2 }),
    (error) => error instanceof APIError && error.isConflict,
  );
  assert(stale.calls.every((call) => call.path !== "/deployments"));
});
test("service edits preserve siblings and runtime actions carry the explicit application revision", async () => {
  const { client, calls } = fixture((call) => {
    if (call.path === "/plan")
      return Response.json({ ...plan, spec: call.body.spec });
    if (call.path.endsWith("/scale"))
      return Response.json({
        id: "fixture-scaled",
        application_id: application.id,
      });
    return Response.json(application);
  });
  const web = client.application(application.id).service("web");
  const review = await web.plan({ replicas: 2 });
  assert.deepEqual(review.plan.spec.services.worker, spec.services.worker);
  assert.equal(review.plan.spec.services.web.replicas, 2);
  assert.deepEqual(calls.find((call) => call.path === "/plan").body.services, [
    "web",
  ]);
  await web.scale(3, { expectedRevision: 3, idempotencyKey: "fixture-scale" });
  assert.deepEqual(calls.at(-1).body, { expected_revision: 3, replicas: 3 });
  assert.equal(calls.at(-1).key, "fixture-scale");
  assert.throws(() => web.scale(0, { expectedRevision: 3 }));
});
test("named application lookup follows cursors and never falls back to another scope", async () => {
  const { client, calls } = fixture((call) =>
    Response.json(
      call.path === "/applications"
        ? call.url.searchParams.has("cursor")
          ? { items: [application] }
          : { items: [], next_cursor: "page-two" }
        : application,
    ),
  );
  assert.equal((await client.app("fixture-app").get()).id, application.id);
  assert.equal(calls[1].url.searchParams.get("cursor"), "page-two");
  const wrong = fixture(() =>
    Response.json({ ...application, environment: "production" }),
  );
  await assert.rejects(wrong.client.application(application.id).get(), {
    code: "scope_mismatch",
  });
});
test("deployment waits report observed failures, time out honestly and can resume by ID", async () => {
  let status = "queued";
  const { client } = fixture(() =>
    Response.json({ id: "fixture-deploy", status, events: [] }),
  );
  const events = [];
  const done = await client.deployment("fixture-deploy").wait({
    pollIntervalMs: 10,
    onProgress: (item) => {
      events.push(item.status);
      status = "succeeded";
    },
  });
  assert.equal(done.status, "succeeded");
  assert.deepEqual(events, ["queued", "succeeded"]);
  for (status of ["failed", "cancelled", "superseded", "unknown-future-state"])
    await assert.rejects(
      client.deployment("fixture-deploy").wait(),
      (error) =>
        error instanceof OperationError &&
        error.operationId === "fixture-deploy",
    );
  status = "running";
  await assert.rejects(
    client
      .deployment("fixture-deploy")
      .wait({ timeoutMs: 15, pollIntervalMs: 10 }),
    (error) => error instanceof WaitError && error.code === "wait_timeout",
  );
  const controller = new AbortController();
  controller.abort();
  await assert.rejects(
    client.deployment("fixture-deploy").wait({ signal: controller.signal }),
    { code: "wait_aborted" },
  );
  status = "succeeded";
  assert.equal(
    (await client.deployment("fixture-deploy").wait()).status,
    "succeeded",
  );
});
test("database deletion waits for an operation result, never treats missing resources as success", async () => {
  const operation = {
    id: "fixture-delete",
    database_id: database.id,
    status: "succeeded",
    kind: "delete",
  };
  const { client, calls } = fixture((call) =>
    Response.json(
      call.path.startsWith("/database-operations/") ? operation : database,
    ),
  );
  assert.equal(
    (await client.databaseOperation(database.id, operation.id).wait()).status,
    "succeeded",
  );
  assert.equal(calls[0].path, "/database-operations/fixture-delete");
  const hidden = fixture(() =>
    Response.json(
      { error: { code: "not_found", message: "not found" } },
      { status: 404 },
    ),
  );
  await assert.rejects(
    hidden.client.databaseOperation(database.id, operation.id).wait(),
    (error) => error instanceof APIError && error.status === 404,
  );
});
test("database creation retains its recovery key and bindings never fetch passwords", async () => {
  const { client, calls } = fixture((call) =>
    Response.json(
      call.path === "/databases"
        ? { id: "fixture-create", database_id: database.id }
        : database,
    ),
  );
  const created = await client
    .db("fixture-db")
    .create({
      engine: "postgresql",
      cpu: "100m",
      memory: "256Mi",
      storageGiB: 1,
    });
  assert.equal(created.idempotencyKey, calls[0].key);
  assert.deepEqual(calls[0].body.spec, database.spec);
  assert.deepEqual(await created.database.binding(), {
    managed_database: database.id,
    protocol: "postgres",
    endpoint: "read_write",
  });
  assert(calls.every((call) => !call.path.endsWith("/credentials")));
  const redis = fixture(() =>
    Response.json({
      ...database,
      spec: { ...database.spec, engine: "redis", mode: "cluster" },
    }),
  );
  await assert.rejects(redis.client.database(database.id).binding(), {
    code: "cluster_client_required",
  });
  assert.equal(
    (await redis.client.database(database.id).binding({ clusterAware: true }))
      .endpoint,
    "cluster",
  );
});
test("blocked resize reviews cannot apply; approved reviews retain the server's exact ID and spec", async () => {
  let blocked = ["A verified recent backup is required."];
  const { client, calls } = fixture((call) => {
    if (call.path.endsWith("/resize-plan"))
      return Response.json({
        id: "fixture-review",
        plan: {
          expected_revision: 1,
          proposed: call.body.spec,
          blocked_reasons: blocked,
          warnings: [],
        },
      });
    if (call.path.endsWith("/resize"))
      return Response.json({ id: "fixture-resize", database_id: database.id });
    return Response.json(database);
  });
  const ref = client.database(database.id);
  await assert.rejects((await ref.resizePlan({ storageGiB: 2 })).apply(), {
    code: "plan_blocked",
  });
  assert(calls.every((call) => !call.path.endsWith("/resize")));
  blocked = [];
  const review = await ref.resizePlan({ storageGiB: 2 });
  await review.apply();
  assert.deepEqual(calls.at(-1).body, {
    review_id: "fixture-review",
    expected_revision: 1,
    spec: { ...database.spec, storage_gib: 2 },
  });
});
test("network creation and updates are bound to the reviewed identity and revision", async () => {
  let expected = 0;
  const definition = network({
    name: "fixture-net",
    segments: { app: { applications: ["fixture-app"] } },
  });
  const { client, calls } = fixture((call) =>
    Response.json(
      call.path === "/virtual-networks/plan"
        ? {
            spec: definition,
            previous: null,
            toml: "",
            expected_revision: expected,
            expected_id: expected ? "fixture-network-id" : "",
          }
        : { name: definition.name, revision: expected + 1 },
    ),
  );
  const ref = client.network("fixture-net");
  await (await ref.plan({ segments: definition.segments })).apply();
  assert.equal(calls.at(-1).method, "POST");
  assert.equal(calls.at(-1).body.expected_id, "");
  expected = 2;
  await (await ref.plan({ segments: definition.segments })).apply();
  assert.equal(calls.at(-1).method, "PUT");
  assert.equal(calls.at(-1).path, "/virtual-networks/fixture-net");
  assert.equal(calls.at(-1).body.expected_revision, 2);
  assert.equal(calls.at(-1).body.expected_id, "fixture-network-id");
  assert.throws(
    () =>
      ref.delete({
        expectedId: "fixture-network-id",
        expectedRevision: 2,
        confirmName: "wrong",
      }),
    { code: "confirmation_required" },
  );
});
