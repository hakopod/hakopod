import assert from "node:assert/strict";
import test from "node:test";
import { createServer } from "node:http";
import {
  Hakopod,
  APIError,
  HakopodError,
  TransportError,
} from "../dist/index.js";

const key = "hp_explicit_test_fixture";
const options = {
  apiUrl: "https://api.fixture.invalid",
  apiKey: key,
  maxRetries: 0,
};
test("Cloud approval errors preserve the review identity without approving writes", async () => {
  let calls = 0;
  const approval = { id: "a".repeat(32), workspace: "b".repeat(32) };
  const hako = new Hakopod({ ...options, fetch: async () => {
    calls++;
    return Response.json({ error: { code: "approval_required", message: "Review this change in Cloud", approval_id: approval.id, workspace: approval.workspace } }, { status: 409 });
  }});
  await assert.rejects(hako.request("POST", "/deployments", { body: {} }), error => {
    assert(error instanceof APIError);
    assert.deepEqual(error.approval, approval);
    return true;
  });
  assert.equal(calls, 1);
});
test("origin and API-root URLs produce the same scoped request without cookies or redirects", async () => {
  for (const apiUrl of [
    "https://api.fixture.invalid",
    "https://api.fixture.invalid/api/v1/",
  ]) {
    let called = 0;
    const hako = new Hakopod({
      ...options,
      apiUrl,
      workspace: "a".repeat(32),
      fetch: async (url, init) => {
        called++;
        assert.equal(
          String(url),
          "https://api.fixture.invalid/api/v1/applications?project=fixture&environment=development",
        );
        assert.equal(init.headers.get("Authorization"), "Bearer " + key);
        assert.equal(init.headers.get("X-Hakopod-Workspace"), "a".repeat(32));
        assert.equal(init.headers.get("Cookie"), null);
        assert.equal(init.redirect, "manual");
        assert.equal(init.credentials, "omit");
        assert.equal(init.cache, "no-store");
        return Response.json({ items: [] });
      },
    }).in({ project: "fixture", environment: "development" });
    assert.deepEqual(await hako.listApplications(), { items: [] });
    assert.equal(called, 1);
    assert(!JSON.stringify(hako).includes(key));
  }
});
test("unsafe URL credentials, insecure origins and missing scope fail before sending", async () => {
  for (const apiUrl of [
    "http://public.fixture.invalid",
    "https://user:password@fixture.invalid",
    "https://fixture.invalid/?token=secret",
    "https://fixture.invalid/#secret",
    "file:///tmp/api",
    "not a URL",
  ]) {
    assert.throws(
      () => new Hakopod({ ...options, apiUrl }),
      (error) =>
        error instanceof HakopodError && !error.message.includes("password"),
    );
  }
  for (const apiUrl of [
    "http://127.0.0.1:1234",
    "http://localhost:1234",
    "http://[::1]:1234",
  ])
    assert.doesNotThrow(() => new Hakopod({ ...options, apiUrl }));
  assert.throws(() => new Hakopod({ ...options, workspace: "wrong" }));
  assert.throws(() => new Hakopod({ ...options, project: "fixture" }));
  assert.throws(() => new Hakopod(options).app("fixture"), {
    code: "missing_scope",
  });
});
test("route parameters cannot escape the API root, and false/zero query values survive", async () => {
  let count = 0;
  const hako = new Hakopod({
    ...options,
    fetch: async (url) => {
      count++;
      assert.equal(new URL(url).search, "?tail=0&follow=false&service=fixture");
      return Response.json({});
    },
  });
  for (const id of ["..", ".", "a/b", "a\\b", "a?x=1", "%2e%2e"])
    await assert.rejects(
      hako.request("GET", "/applications/{id}", { params: { id } }),
      { code: "invalid_path" },
    );
  await assert.rejects(hako.request("GET", "//evil.invalid"), {
    code: "invalid_path",
  });
  await hako.request("GET", "/applications", {
    query: { tail: 0, follow: false, service: "fixture" },
  });
  assert.equal(count, 1);
});
test("a real redirect never sends the key to its target", async (t) => {
  let target = 0;
  const server = createServer((req, res) => {
    if (req.url === "/api/v1/me") {
      res.writeHead(302, { Location: "/trap" });
      res.end();
    } else {
      target++;
      res.end("must not receive credentials");
    }
  });
  await new Promise((resolve) => server.listen(0, "127.0.0.1", resolve));
  t.after(() => new Promise((resolve) => server.close(resolve)));
  const hako = new Hakopod({
    ...options,
    apiUrl: `http://127.0.0.1:${server.address().port}`,
  });
  await assert.rejects(hako.me(), { code: "redirect_refused" });
  assert.equal(target, 0);
});
test("deployment recovery keys retain supported punctuation through dashboard routes", async () => {
  const recoveryKey = "ci:release_2026.09-".padEnd(128, "a");
  assert.equal(recoveryKey.length, 128);
  const hako = new Hakopod({
    ...options,
    fetch: async (url) => {
      assert.equal(new URL(url).pathname, "/api/v1/idempotency/" + recoveryKey);
      return Response.json({ id: "fixture-deployment" });
    },
  });
  assert.equal(
    (await hako.recoverDeployment(recoveryKey)).id,
    "fixture-deployment",
  );
});
test("only reads retry transient failures; writes retain their recovery key", async () => {
  let calls = 0;
  const hako = new Hakopod({
    ...options,
    maxRetries: 1,
    fetch: async () => {
      calls++;
      return calls === 1
        ? Response.json(
            { error: { code: "busy", message: "Retry later" } },
            { status: 503, headers: { "Retry-After": "0" } },
          )
        : Response.json({ id: "fixture" });
    },
  });
  assert.equal((await hako.me()).id, "fixture");
  assert.equal(calls, 2);
  calls = 0;
  const writing = new Hakopod({
    ...options,
    maxRetries: 5,
    fetch: async () => {
      calls++;
      throw new Error("unsafe transport text " + key);
    },
  });
  await assert.rejects(
    writing.request("POST", "/deployments", {
      body: {},
      idempotencyKey: "fixture-recovery-key",
    }),
    (error) => {
      assert(error instanceof TransportError);
      assert.equal(error.outcome, "unknown");
      assert.equal(error.idempotencyKey, "fixture-recovery-key");
      assert(!String(error).includes(key));
      return true;
    },
  );
  assert.equal(calls, 1);
});
test("server conflicts and long Retry-After values remain explicit, without retrying early", async () => {
  for (const status of [401, 403, 409, 429, 503]) {
    let calls = 0;
    const hako = new Hakopod({
      ...options,
      maxRetries: 2,
      fetch: async () => {
        calls++;
        return Response.json(
          { error: { code: "fixture_error", message: "Rejected " + key } },
          { status, headers: { "Retry-After": "60" } },
        );
      },
    });
    await assert.rejects(
      hako.me(),
      (error) =>
        error instanceof APIError &&
        error.status === status &&
        error.retryAfterMs === 60_000 &&
        !error.message.includes(key),
    );
    assert.equal(calls, 1);
  }
});
test("request, streamed response, and timeout budgets are enforced", async () => {
  let sent = 0;
  const hako = new Hakopod({
    ...options,
    maxResponseBytes: 1024,
    fetch: async () => {
      sent++;
      return new Response(
        new ReadableStream({
          start(c) {
            c.enqueue(new Uint8Array(1025));
            c.close();
          },
        }),
        { headers: { "Content-Type": "application/json" } },
      );
    },
  });
  await assert.rejects(
    hako.request("POST", "/plan", { body: { data: "a".repeat(1024 * 1024) } }),
    { code: "body_limit" },
  );
  assert.equal(sent, 0);
  await assert.rejects(hako.me(), { code: "response_limit" });
  const never = new Hakopod({
    ...options,
    timeoutMs: 15,
    fetch: async () => new Promise(() => {}),
  });
  await assert.rejects(
    never.me(),
    (error) =>
      error instanceof TransportError && error.outcome === "read_failed",
  );
  const aborted = new AbortController();
  aborted.abort();
  await assert.rejects(hako.me({ signal: aborted.signal }), {
    code: "request_aborted",
  });
  assert.equal(sent, 1);
});
test("malformed write responses preserve an ambiguous outcome; HTML is never echoed", async () => {
  const hako = new Hakopod({
    ...options,
    fetch: async () =>
      new Response("not-json " + key, {
        status: 202,
        headers: { "Content-Type": "application/json" },
      }),
  });
  await assert.rejects(
    hako.request("POST", "/deployments", {
      body: {},
      idempotencyKey: "fixture-recovery-key",
    }),
    (error) =>
      error instanceof TransportError &&
      error.outcome === "unknown" &&
      error.idempotencyKey === "fixture-recovery-key" &&
      !error.message.includes(key),
  );
  const html = new Hakopod({
    ...options,
    fetch: async () => new Response("<html>" + key, { status: 502 }),
  });
  await assert.rejects(
    html.me(),
    (error) => error instanceof APIError && !error.message.includes(key),
  );
});
test("concurrent callers share a bounded transport, including scoped views", async () => {
  let active = 0,
    maximum = 0;
  const hako = new Hakopod({
    ...options,
    maxConcurrency: 2,
    fetch: async () => {
      maximum = Math.max(maximum, ++active);
      await new Promise((resolve) => setTimeout(resolve, 3));
      active--;
      return Response.json({ id: "fixture" });
    },
  });
  await Promise.all(
    Array.from({ length: 12 }, (_, n) =>
      hako.in({ project: `fixture-${n}`, environment: "development" }).me(),
    ),
  );
  assert.equal(maximum, 2);
});
