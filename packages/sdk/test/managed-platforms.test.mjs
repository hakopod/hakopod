import assert from "node:assert/strict";
import test from "node:test";
import { Hakopod } from "../dist/index.js";

// Synthetic contract fixtures; no running platform is represented here.
const scope = { project: "sdk-fixture", environment: "development" };
const platform = {
  id: "a".repeat(32), ...scope, revision: 7,
  spec: { schema_version: 1, name: "customer-supabase", kind: "supabase", version: "fixture", resources: {}, storage: {}, secrets: {} },
};
function fixture(current) {
  const calls = [];
  const client = new Hakopod({
    apiUrl: "https://fixture.invalid/api/v1", apiKey: "hp_fixture", ...scope, maxRetries: 0,
    fetch: async (url, init) => {
      calls.push({ path: new URL(url).pathname, method: init.method, body: init.body && JSON.parse(init.body) });
      return Response.json(init.method === "GET" ? current : { blocked: false });
    },
  });
  return { client, calls };
}
test("platform delete review carries the fetched revision and complete current spec", async () => {
  const { client, calls } = fixture(platform);
  await client.managedPlatform(platform.id).reviewDelete(platform.spec.name);
  assert.equal(calls[0].path, `/api/v1/managed-platforms/${platform.id}`);
  assert.deepEqual(calls[1], {
    path: "/api/v1/managed-platforms/reviews", method: "POST",
    body: { id: platform.id, ...scope, expected_revision: 7, kind: "delete", confirm_name: platform.spec.name, spec: platform.spec },
  });
});
test("platform delete review rejects stale names and inaccessible scope before POST", async () => {
  for (const current of [platform, { ...platform, project: "another-project" }]) {
    const { client, calls } = fixture(current);
    await assert.rejects(client.managedPlatform(platform.id).reviewDelete(current === platform ? "old-name" : platform.spec.name));
    assert.equal(calls.length, 1);
    assert.equal(calls[0].method, "GET");
  }
});

test("platform list omits scope globally and carries exact selected scope", async () => {
  const queries = [];
  const client = new Hakopod({
    apiUrl: "https://fixture.invalid/api/v1", apiKey: "hp_fixture", maxRetries: 0,
    fetch: async (url) => { queries.push(new URL(url).searchParams); return Response.json({ items: [] }); },
  });
  await client.listManagedPlatforms();
  await client.in(scope).listManagedPlatforms();
  assert.equal(queries[0].toString(), "");
  assert.equal(queries[1].get("project"), scope.project);
  assert.equal(queries[1].get("environment"), scope.environment);
});
