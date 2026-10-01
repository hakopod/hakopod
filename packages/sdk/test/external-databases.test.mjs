import assert from "node:assert/strict";
import test from "node:test";
import { Hakopod, OperationError } from "../dist/index.js";

// Synthetic API responses exercise the SDK; they are not provider acceptance.
const scope = { project: "sdk-fixture", environment: "development" };
const input = { engine: "mysql", host: "fixture.psdb.cloud", port: 3306, database: "app" };
const resource = {
  id: "a".repeat(32), ...scope, revision: 2, credential_revision: 2,
  spec: { ...input, name: "reporting", schema_version: 1, provider: "planetscale" },
  status: "ready", observation: {},
};
function fixture(handler) {
  const calls = [];
  const client = new Hakopod({
    apiUrl: "https://fixture.invalid/api/v1", apiKey: "hp_fixture", ...scope, maxRetries: 0,
    fetch: async (url, init) => {
      const call = { path: new URL(url).pathname.slice(7), method: init.method, key: init.headers.get("Idempotency-Key"), body: init.body && JSON.parse(init.body) };
      calls.push(call);
      return Response.json(await handler(call));
    },
  });
  return { client, calls };
}
test("external references reject a resource returned from another scope", async () => {
  const { client, calls } = fixture(() => ({ ...resource, project: "another-project" }));
  await assert.rejects(client.externalDatabase(resource.id).delete({ expectedRevision: 2, confirmName: "reporting" }), { code: "scope_mismatch" });
  assert.equal(calls.length, 1);
});
test("external review validates disconnect intent and credential revision", async () => {
  const plan = { id: "review", kind: "connect", database_id: resource.id, database_revision: 2, credential_revision: 2, application_id: "app", application_name: "fixture", service: "api", variable: "DATABASE_URL" };
  const { client, calls } = fixture((call) => call.method === "GET" ? resource : plan);
  await assert.rejects(client.externalDatabase(resource.id).connectionPlan({ applicationId: "app", service: "api", variable: "DATABASE_URL", disconnect: false }), { code: "invalid_response" });
  await assert.rejects(client.externalDatabase(resource.id).connectionPlan({ applicationId: "app", service: "api", variable: "DATABASE_URL", disconnect: true }), { code: "invalid_response" });
  plan.kind = "disconnect";
  plan.credential_revision = 1;
  await assert.rejects(client.externalDatabase(resource.id).connectionPlan({ applicationId: "app", service: "api", variable: "DATABASE_URL", disconnect: true }), { code: "invalid_response" });
});
test("external review reuses its key and preserves disconnect confirmation", async () => {
  const plan = { id: "review", kind: "disconnect", database_id: resource.id, database_revision: 2, credential_revision: 2, application_id: "app", application_name: "fixture", service: "api", variable: "DATABASE_URL" };
  const { client, calls } = fixture((call) => call.method === "GET" ? resource : call.path.endsWith("connection-plan") ? plan : { id: "deployment", application_id: "app", status: "queued" });
  const review = await client.externalDatabase(resource.id).connectionPlan({ applicationId: "app", service: "api", variable: "DATABASE_URL", disconnect: true });
  await review.apply();
  await review.apply();
  const mutations = calls.filter((call) => call.path.endsWith("/connect"));
  assert.equal(mutations.length, 2);
  assert.equal(mutations[0].key, mutations[1].key);
  assert.deepEqual(mutations[0].body, { review_id: "review", confirm_application: "fixture" });
});
test("legacy external records can still be inspected and deleted", async () => {
  const { client, calls } = fixture((call) => call.method === "GET" ? resource : { id: "operation", database_id: resource.id, status: "queued" });
  const ref = client.externalDatabase(resource.id);
  assert.equal((await ref.get()).id, resource.id);
  const operation = await ref.delete({ expectedRevision: 2, confirmName: "reporting" });
  assert.equal(operation.databaseId, resource.id);
  assert.equal(calls.find((call) => call.method === "DELETE").body.expected_revision, 2);
});
test("legacy external credentials can still be rotated without changing the spec", async () => {
  const { client, calls } = fixture((call) => call.method === "GET" ? resource : { id: "operation", database_id: resource.id, status: "queued" });
  const operation = await client.externalDatabase(resource.id).rotateCredentials({ credentials: { username: "rotated-user", password: "rotated-password" }, expectedRevision: 2, confirmName: "reporting" });
  assert.equal(operation.databaseId, resource.id);
  const mutation = calls.find((call) => call.method === "PUT");
  assert.deepEqual(mutation.body.spec, resource.spec);
  assert.deepEqual(mutation.body.credentials, { username: "rotated-user", password: "rotated-password" });
});
test("external operation wait reports verification failure and rejects another database", async () => {
  let response = { id: "operation", database_id: "other", status: "succeeded" };
  const { client } = fixture(() => response);
  const operation = client.externalDatabaseOperation(resource.id, "operation");
  await assert.rejects(operation.get(), { code: "invalid_response" });
  response = { ...response, database_id: resource.id, status: "failed" };
  await assert.rejects(operation.wait(), OperationError);
});
