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
test("managed platform catalog requires and preserves an explicit scope", async () => {
  const { client, calls } = fixture(() => Response.json({ items: [] }));
  await client.in({ project: "orders", environment: "staging" }).managedPlatformCatalog();
  await client.managedPlatformCatalog();
  assert.equal(calls.length, 2);
  for (const call of calls) {
    assert.equal(call.path, "/managed-platforms/catalog");
    assert.equal(call.method, "GET");
  }
  assert.equal(calls[0].url.searchParams.get("project"), "orders");
  assert.equal(calls[0].url.searchParams.get("environment"), "staging");
  assert.equal(calls[1].url.searchParams.get("project"), scope.project);
  assert.equal(calls[1].url.searchParams.get("environment"), scope.environment);
  const unscoped = new Hakopod({ apiUrl: "https://fixture.invalid", apiKey: "hp_fixture", fetch: async () => { throw new Error("unscoped catalog must not be sent"); } });
  assert.throws(() => unscoped.managedPlatformCatalog(), { code: "missing_scope" });
});
test("database node discovery uses the selected scope without changing the client's scope", async () => {
  const { client, calls } = fixture(() => Response.json({ items: [], limit: 48 }));
  assert.deepEqual(await client.in({ project: "orders", environment: "staging" }).databasePlacementNodes(), { items: [], limit: 48 });
  await client.databasePlacementNodes();
  for (const call of calls) {
    assert.equal(call.path, "/database-placement/nodes");
    assert.equal(call.method, "GET");
  }
  assert.equal(calls[0].url.searchParams.get("project"), "orders");
  assert.equal(calls[0].url.searchParams.get("environment"), "staging");
  assert.equal(calls[1].url.searchParams.get("project"), scope.project);
  assert.equal(calls[1].url.searchParams.get("environment"), scope.environment);
});
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
  await client.application(application.id).delete({
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
  await client.database(database.id).schedule({
    name: "daily-fixture",
    destinationId: "destination-fixture",
    frequency: "daily",
    retentionCount: 7,
  });
  assert.equal(calls.at(-1).body.interval_hours, 24);
});
test("database public endpoint review binds scope and preserves one retry key", async () => {
  const endpointPlan = {
    id: "endpoint-review",
    plan: {
      database_id: database.id,
      project: scope.project,
      environment: scope.environment,
      database_revision: database.revision,
      endpoint_id: "endpoint-fixture",
      endpoint_revision: 0,
      spec: { purpose: "read_write", source_cidrs: ["192.0.2.0/24"], max_connections: 32 },
      route: { purpose: "read_write", protocol: "postgresql", routing: "direct", read_only: false, pooled: false },
      allocation: { id: "allocation-fixture", host: "database-15432.example.test", address: "192.0.2.10", port: 15432 },
      topology_fingerprint: "topology-fixture",
      tls_fingerprint: "tls-fixture",
      blocked_reasons: [],
      warnings: ["Existing connections close before publication."],
      expires_at: new Date(Date.now() + 60_000).toISOString(),
    },
  };
  const accepted = { id: "endpoint-operation", endpoint_id: endpointPlan.plan.endpoint_id, database_id: database.id, revision: 1, kind: "publish", status: "queued", phase: "accepted", message: "", created_at: new Date().toISOString() };
  const capabilities = {
    engine: "postgresql",
    available: true,
    unavailable_reason: "",
    routes: [
      { purpose: "read_write", protocol: "postgresql", routing: "direct", read_only: false, pooled: false },
    ],
  };
  const { client, calls } = fixture((call) => Response.json(
    call.path.endsWith("public-endpoint-plan") ? endpointPlan :
      call.path.endsWith("public-endpoint-capabilities") ? capabilities :
      call.path.endsWith("public-endpoints") && call.method === "POST" ? accepted : database,
  ));
  const input = { purpose: "read_write", source_cidrs: ["192.0.2.0/24"], max_connections: 32 };
  const review = await client.database(database.id).publicEndpointPlan(input);
  input.source_cidrs[0] = "0.0.0.0/0";
  const run = await review.apply();
  await review.apply();
  assert.equal(run.endpointId, endpointPlan.plan.endpoint_id);
  assert.equal(calls[3].key, calls[4].key);
  assert.deepEqual(calls[3].body, { review_id: endpointPlan.id, expected_database_revision: 1, expected_endpoint_revision: 0 });
});
test("database public endpoint capabilities preserve the selected database identity", async () => {
  const capabilities = {
    engine: "mysql",
    available: false,
    unavailable_reason: "MySQL public endpoint qualification is incomplete.",
    routes: [
      { purpose: "read_write", protocol: "mysql", routing: "mysql_router", read_only: false, pooled: false },
      { purpose: "read_only", protocol: "mysql", routing: "mysql_router", read_only: true, pooled: false },
    ],
  };
  const mysql = { ...database, spec: { ...database.spec, engine: "mysql" } };
  const { client, calls } = fixture((call) => Response.json(
    call.path.endsWith("public-endpoint-capabilities") ? capabilities : mysql,
  ));
  assert.deepEqual(await client.database(mysql.id).publicEndpointCapabilities(), capabilities);
  assert.equal(calls.at(-1).method, "GET");
  assert.equal(calls.at(-1).path, `/databases/${mysql.id}/public-endpoint-capabilities`);
});
test("database public endpoint review rejects missing or changed route descriptors", async () => {
  const route = { purpose: "read_write", protocol: "postgresql", routing: "direct", read_only: false, pooled: false };
  for (const reviewedRoute of [undefined, { ...route, protocol: "mysql" }, { ...route, routing: "pgbouncer" }, { ...route, read_only: true }, { ...route, pooled: true }]) {
    const input = { purpose: "read_write", source_cidrs: ["192.0.2.0/24"], max_connections: 32 };
    const { client, calls } = fixture((call) => Response.json(
      call.path.endsWith("public-endpoint-capabilities") ? { engine: database.spec.engine, available: true, routes: [route], unavailable_reason: "" } :
        call.path.endsWith("public-endpoint-plan") ? { id: "review", plan: { database_id: database.id, project: database.project, environment: database.environment, database_revision: database.revision, spec: input, route: reviewedRoute } } : database,
    ));
    await assert.rejects(client.database(database.id).publicEndpointPlan(input), { code: "invalid_response" });
    assert.equal(calls.some((call) => call.path.endsWith("public-endpoints") && call.method === "POST"), false);
  }
});
test("database public endpoint revocation and operation lookup preserve identities", async () => {
  const endpointId = "endpoint-fixture";
  const accepted = { id: "revoke-operation", endpoint_id: endpointId, database_id: database.id, revision: 3, kind: "revoke", status: "queued", phase: "accepted", message: "", created_at: new Date().toISOString() };
  const { client, calls } = fixture((call) => Response.json(call.method === "GET" && call.path === `/databases/${database.id}` ? database : accepted));
  const run = await client.database(database.id).revokePublicEndpoint(endpointId, 2, { idempotencyKey: "endpoint-revoke-key" });
  assert.equal(calls.at(-1).path, `/databases/${database.id}/public-endpoints/${endpointId}`);
  assert.deepEqual(calls.at(-1).body, { expected_endpoint_revision: 2 });
  assert.equal(calls.at(-1).key, "endpoint-revoke-key");
  assert.equal((await run.get()).id, accepted.id);
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
    client.database(database.id).inspectRecovery({
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
  const created = await client.db("fixture-db").create({
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
test("Vitess preserves reviewed configuration and uses MySQL gateway routes", async () => {
  const vitess = { backup_destination_id: 'a'.repeat(32), backup_destination_revision: 3, tables: [{ name: 'orders', sharding_column: 'tenant_id' }] };
  const definition = db({ name: 'vitess-fixture', engine: 'vitess', mode: 'cluster', shards: 2, cpu: '500m', memory: '1Gi', storageGiB: 5, vitess });
  assert.equal(definition.version, '23');
  assert.equal(definition.replicas, 1);
  assert.deepEqual(definition.vitess, vitess);
  assert.deepEqual(definition.tls, { mode: 'required' });
  const f = fixture(() => Response.json({ ...database, spec: definition }));
  assert.deepEqual(await f.client.database(database.id).binding(), { managed_database: database.id, protocol: 'mysql', endpoint: 'read_write' });
  assert.deepEqual(await f.client.database(database.id).binding({ endpoint: 'read_only' }), { managed_database: database.id, protocol: 'mysql', endpoint: 'read_only' });
  await assert.rejects(f.client.database(database.id).binding({ endpoint: 'cluster', clusterAware: true }), { code: 'invalid_binding' });
});
test("MyDuck uses its pinned version and exposes only managed MySQL and PostgreSQL bindings", async () => {
  const definition = db({ name: "myduck-fixture", engine: "duckdb", cpu: "500m", memory: "1Gi", storageGiB: 5 });
  assert.equal(definition.version, "0.3.1-dev.20260919.3");
  assert.equal(definition.mode, "standalone");
  assert.equal(definition.replicas, 0);
  assert.equal(definition.shards, 1);
  assert.deepEqual(definition.tls, { mode: "required" });
  const { client } = fixture(() => Response.json({ ...database, spec: definition }));
  assert.deepEqual(await client.database(database.id).binding(), {
    managed_database: database.id,
    protocol: "mysql",
    endpoint: "mysql",
    username: "root",
    database: "app",
  });
  assert.deepEqual(await client.database(database.id).binding({ endpoint: "postgresql", sslMode: "verify-full" }), {
    managed_database: database.id,
    protocol: "postgres",
    endpoint: "postgresql",
    username: "postgres",
    database: "app",
    ssl_mode: "verify-full",
  });
  assert.deepEqual(await client.database(database.id).binding({ endpoint: "mysql", username: "", database: "" }), {
    managed_database: database.id,
    protocol: "mysql",
    endpoint: "mysql",
    username: "root",
    database: "app",
  });
  assert.deepEqual(await client.database(database.id).binding({ endpoint: "postgresql", username: "", database: "" }), {
    managed_database: database.id,
    protocol: "postgres",
    endpoint: "postgresql",
    username: "postgres",
    database: "app",
  });
  for (const options of [
    { endpoint: "read_write" },
    { endpoint: "read_only" },
    { endpoint: "cluster", clusterAware: true },
    { endpoint: "pooled_read_write" },
    { endpoint: "mysql", username: "app" },
    { endpoint: "postgresql", username: "root" },
    { endpoint: "mysql", database: "analytics" },
    { endpoint: "mysql", password: { ref: "custom-password" } },
    { endpoint: "mysql", sslMode: "verify-full" },
    { endpoint: "postgresql", sslMode: "require" },
    { endpoint: "postgresql", sslMode: "verify-ca" },
    { endpoint: "postgresql", sslMode: "disable" },
  ])
    await assert.rejects(client.database(database.id).binding(options), { code: "invalid_binding" });
});
test("ClickHouse and Oracle SDK definitions preserve engine configuration and routing", async () => {
  for (const engine of ["clickhouse", "oracle"]) {
    const definition = db({ name: "engine-fixture", engine, cpu: "1", memory: "4Gi", storageGiB: 10 });
    assert.equal(definition.version, engine === "oracle" ? "23.26" : "26.3");
    assert.deepEqual(definition.tls, { mode: "required" });
    if (engine === "oracle") assert.deepEqual(definition.oracle, { edition: "free" });
    const f = fixture(() => Response.json({ ...database, spec: definition }));
    assert.deepEqual(await f.client.database(database.id).binding(), { managed_database: database.id, protocol: engine, endpoint: "read_write" });
    await assert.rejects(f.client.database(database.id).binding({ endpoint: "read_only" }), { code: "invalid_binding" });
  }
  const oracle = { edition: "enterprise", image: `registry.example.com/oracle@sha256:${"a".repeat(64)}`, registry_credential: "oracle-registry", license_confirmed: true };
  assert.deepEqual(db({ name: "licensed-fixture", engine: "oracle", oracle, version: "19", cpu: "1", memory: "4Gi", storageGiB: 20 }).oracle, oracle);
  const cluster = fixture(() => Response.json({ ...database, spec: db({ name: "cluster-fixture", engine: "clickhouse", mode: "cluster", shards: 2, cpu: "1", memory: "4Gi", storageGiB: 10 }) }));
  await assert.rejects(cluster.client.database(database.id).binding(), { code: "cluster_client_required" });
  assert.deepEqual(await cluster.client.database(database.id).binding({ clusterAware: true }), { managed_database: database.id, protocol: "clickhouse", endpoint: "cluster", cluster_aware: true });
  await assert.rejects(cluster.client.database(database.id).binding({ endpoint: "read_only", clusterAware: true }), { code: "invalid_binding" });
});
test("MySQL SDK creation and binding retain its engine, quorum, TLS and placement", async () => {
  const input = {
    engine: "mysql",
    mode: "cluster",
    cpu: "500m",
    memory: "1Gi",
    storageGiB: 5,
    placement: { spread: "zones" },
  };
  const mysql = { ...database, spec: db({ ...input, name: "mysql-fixture" }) };
  const { client, calls } = fixture((call) =>
    Response.json(
      call.path === "/databases"
        ? { id: "mysql-create", database_id: mysql.id }
        : mysql,
    ),
  );
  await client.db("mysql-fixture").create(input);
  assert.equal(calls[0].body.spec.version, "8.4");
  assert.equal(calls[0].body.spec.replicas, 2);
  assert.deepEqual(calls[0].body.spec.tls, { mode: "required" });
  assert.deepEqual(calls[0].body.spec.placement, { spread: "zones" });
  assert.deepEqual(
    await client.database(mysql.id).binding({ endpoint: "read_only" }),
    { managed_database: mysql.id, protocol: "mysql", endpoint: "read_only" },
  );
  for (const options of [
    { endpoint: "cluster" },
    { endpoint: "pooled_read_write" },
    { clusterAware: true },
  ])
    await assert.rejects(client.database(mysql.id).binding(options), {
      code: "invalid_binding",
    });
  assert(calls.every((call) => !call.path.endsWith("/credentials")));
});
test("SDK preserves PgBouncer configuration and validates routes against it", async () => {
  const pooling = {
    mode: "transaction",
    instances: 2,
    max_client_connections: 100,
    default_pool_size: 10,
    read_only: true,
  };
  const spec = db({
    name: "pooled-fixture",
    engine: "postgresql",
    mode: "cluster",
    cpu: "250m",
    memory: "512Mi",
    storageGiB: 5,
    pooling,
  });
  assert.deepEqual(spec.pooling, pooling);
  const { client } = fixture(() => Response.json({ ...database, spec }));
  assert.equal(
    (
      await client
        .database(database.id)
        .binding({ endpoint: "pooled_read_only" })
    ).endpoint,
    "pooled_read_only",
  );
  const standalone = fixture(() => Response.json(database));
  for (const endpoint of ["read_only", "pooled_read_write", "cluster"])
    await assert.rejects(
      standalone.client.database(database.id).binding({ endpoint }),
      { code: "invalid_binding" },
    );
});

test("MongoDB SDK uses replica-set discovery even for standalone deployments", async () => {
  for (const mode of ["standalone", "cluster"]) {
    const spec = db({ name: "mongo-fixture", engine: "mongodb", mode, cpu: "500m", memory: "1Gi", storageGiB: 5 });
    assert.equal(spec.version, "8.0");
    assert.equal(spec.replicas, mode === "cluster" ? 2 : 0);
    assert.deepEqual(spec.tls, { mode: "required" });
    const { client, calls } = fixture(() => Response.json({ ...database, spec }));
    await assert.rejects(client.database(database.id).binding(), { code: "cluster_client_required" });
    assert.deepEqual(await client.database(database.id).binding({ clusterAware: true }), { managed_database: database.id, protocol: "mongodb", endpoint: "cluster", cluster_aware: true });
    for (const endpoint of ["read_write", "read_only", "pooled_read_write"]) await assert.rejects(client.database(database.id).binding({ endpoint, clusterAware: true }), { code: "invalid_binding" });
    assert(calls.every((call) => !call.path.endsWith("/credentials")));
  }
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
