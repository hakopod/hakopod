import assert from "node:assert/strict";
import test from "node:test";
import { Hakopod } from "../dist/index.js";

// Explicit API contract fixtures; these do not represent a running database.
function fixture() {
  const calls = [];
  const client = new Hakopod({
    apiUrl: "https://fixture.invalid/api/v1", apiKey: "hp_fixture",
    project: "binding-fixture", environment: "development", maxRetries: 0,
    fetch: async (url, init) => {
      calls.push({ path: new URL(url).pathname, method: init.method, body: init.body && JSON.parse(init.body) });
      if (init.method === "GET") return Response.json({
        id: "application-fixture", project: "binding-fixture", environment: "development", revision: 7,
        spec: { services: { api: { bindings: { DATABASE_URL: { protocol: "postgres", managed_database: "a".repeat(32), endpoint: "read_write" } } } } },
      });
      return Response.json({ schema_version: 1, application_id: "application-fixture", service: "api", variable: "DATABASE_URL", revision: 7, pod: "api-fixture-1", outcome: "failed", stages: [{ name: "authentication", status: "failed", code: "authentication_rejected", message: "The server rejected the loaded credentials." }] });
    },
  });
  return { client, calls };
}

test("connection testing pins the saved revision and never sends connection values", async () => {
  const { client, calls } = fixture();
  const result = await client.application("application-fixture").service("api").testConnection("DATABASE_URL", { pod: "api-fixture-1" });
  assert.equal(result.outcome, "failed");
  assert.deepEqual(calls[1], {
    path: "/api/v1/applications/application-fixture/services/api/bindings/DATABASE_URL/test",
    method: "POST", body: { expected_revision: 7, pod: "api-fixture-1" },
  });
  assert.equal(calls.length, 2);
});

test("connection testing rejects undeclared variables before issuing an execution request", async () => {
  const { client, calls } = fixture();
  await assert.rejects(client.application("application-fixture").service("api").testConnection("UNRELATED_SECRET"), { code: "binding_not_found" });
  assert.equal(calls.length, 1);
  assert.equal(calls[0].method, "GET");
});
