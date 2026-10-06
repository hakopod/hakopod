import assert from "node:assert/strict";
import test from "node:test";
import { Hakopod } from "../dist/index.js";

// Synthetic contract fixtures; no database or credentials are read.
const scope = { project: "binding-fixture", environment: "development" };
const database = {
  id: "a".repeat(32), ...scope, revision: 1, status: "ready",
  spec: { engine: "postgresql", name: "fixture-db", mode: "standalone", replicas: 0, tls: { mode: "required" } },
};
function fixture(changePlan = (plan) => plan) {
  const calls = [];
  const client = new Hakopod({
    apiUrl: "https://fixture.invalid/api/v1", apiKey: "hp_fixture", ...scope, maxRetries: 0,
    fetch: async (url, init) => {
      const body = init.body && JSON.parse(init.body);
      calls.push({ path: new URL(url).pathname, method: init.method, body });
      if (init.method === "GET") return Response.json(database);
      return Response.json(changePlan({
        id: "review", database_id: database.id, application_id: body.application_id,
        service: body.service, variable: body.variable,
        binding: { managed_database: database.id, protocol: "postgres", endpoint: body.endpoint,
          cluster_aware: body.cluster_aware, username: body.username, database: body.database,
          password: body.password, ssl_mode: body.ssl_mode },
      }));
    },
  });
  return { client, calls };
}
const options = { username: "infisical_user", database: "infisical", password: { ref: "infisical-password" }, sslMode: "verify-full" };
const input = { applicationId: "app-fixture", service: "api", variable: "DB_CONNECTION_URI", ...options };

test("bindings preserve selected identity and secret reference without fetching credentials", async () => {
  const { client, calls } = fixture();
  const binding = await client.database(database.id).binding(options);
  assert.deepEqual(binding, { managed_database: database.id, protocol: "postgres", endpoint: "read_write",
    username: options.username, database: options.database, password: options.password, ssl_mode: options.sslMode });
  assert.notEqual(binding.password, options.password);
  assert(calls.every((call) => call.method === "GET" && !call.path.endsWith("/credentials")));
  for (const password of ["plaintext", { ref: "ref", value: "plaintext" }, { ref: "ref", provider: "mixed" }]) {
    await assert.rejects(client.database(database.id).binding({ ...options, password }), { code: "invalid_binding" });
  }
});
test("connection review sends and verifies all selected binding options", async () => {
  const { client, calls } = fixture();
  await client.database(database.id).connectionPlan(input);
  const request = calls.find((call) => call.method === "POST");
  assert.equal(request.body.username, options.username);
  assert.equal(request.body.database, options.database);
  assert.equal(request.body.ssl_mode, options.sslMode);
  assert.deepEqual(request.body.password, options.password);
  for (const change of [
    (p) => ({ ...p, binding: { ...p.binding, database: "app" } }),
    (p) => ({ ...p, binding: { ...p.binding, password: { ref: "another" } } }),
    (p) => ({ ...p, binding: { ...p.binding, ssl_mode: "require" } }),
  ]) {
    const altered = fixture(change);
    await assert.rejects(altered.client.database(database.id).connectionPlan(input), { code: "invalid_response" });
    assert(altered.calls.every((call) => !call.path.endsWith("/connect")));
  }
});
