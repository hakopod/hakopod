import test from "node:test";
import assert from "node:assert/strict";
import { mkdtemp, chmod, symlink, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import {
  API,
  APIError,
  origin,
  remote,
  deviceLogin,
  writePrivate,
  readPrivate,
  profilePath,
} from "../src/core.mjs";

test("origins reject embedded credentials, paths, queries and non-loopback HTTP", () => {
  for (const value of [
    "http://example.com",
    "https://user:pass@example.com",
    "https://example.com/api",
    "https://example.com?token=x",
    "https://example.com#x",
    "file:///tmp/a",
  ])
    assert.throws(() => origin(value));
  assert.equal(origin("https://example.com/"), "https://example.com");
  assert.equal(origin("http://127.0.0.1:1234"), "http://127.0.0.1:1234");
  assert.notEqual(
    profilePath("https://a.example"),
    profilePath("https://b.example"),
  );
});
test("Git remotes accept supported SSH and HTTPS URLs without embedded credentials", () => {
  for (const value of [
    "git@github.com:owner/repo.git",
    "https://github.com/owner/repo.git",
    "ssh://git@github.com/owner/repo",
  ])
    assert.deepEqual(remote(value), {
      provider: "github",
      repository: "owner/repo",
    });
  assert.deepEqual(remote("git@gitlab.com:group/nested/repo.git"), {
    provider: "gitlab",
    repository: "group/nested/repo",
  });
  for (const value of [
    "https://token@github.com/owner/repo",
    "https://evil.example/repo",
    "-x",
    "https://github.com/../repo",
  ])
    assert.throws(() => remote(value));
});
test("credentials never cross installation origins or follow redirects", async () => {
  let calls = 0;
  const transport = async (_url, options) => {
    calls++;
    assert.equal(options.redirect, "error");
    assert.equal(options.headers["X-Hakopod-Workspace"], "a".repeat(32));
    return Response.json({ ok: true });
  };
  const profile = {
    origin: "https://a.example",
    token: "hs_fixture",
    scope_id: "a".repeat(32),
  };
  await assert.rejects(
    new API("https://b.example", profile, transport).call("/me"),
    /different installation/,
  );
  assert.equal(calls, 0);
  await new API("https://a.example", profile, transport).call("/me");
  assert.equal(calls, 1);
});
test("private state roundtrips atomically and rejects permissive or symlinked files", async () => {
  const dir = await mkdtemp(join(tmpdir(), "hakopod-cli-test-"));
  try {
    const file = join(dir, "profile.json");
    await writePrivate(file, { token: "test-only" });
    assert.deepEqual(await readPrivate(file), { token: "test-only" });
    await writePrivate(file, { token: "replacement" });
    assert.deepEqual(await readPrivate(file), { token: "replacement" });
    await symlink(file, join(dir, "link"));
    await assert.rejects(readPrivate(join(dir, "link")));
    await chmod(file, 0o644);
    if (process.platform !== "win32")
      await assert.rejects(readPrivate(file), /private file/);
  } finally {
    await rm(dir, { recursive: true, force: true });
  }
});
const challenge = {
  device_code: "a".repeat(64),
  user_code: "ABCD-EFGH",
  expires_in: 600,
  interval: 5,
  verification_uri_complete:
    "https://a.example/login/device?user_code=ABCD-EFGH",
};
test("device authorization polls pending and slow-down then returns a scoped session", async () => {
  let time = 0,
    polls = 0;
  const waits = [];
  const api = {
    origin: "https://a.example",
    call: async (path) => {
      if (path.endsWith("start")) return challenge;
      if (++polls === 1)
        throw new APIError(400, { error: { code: "authorization_pending" } });
      if (polls === 2)
        throw new APIError(400, { error: { code: "slow_down" } });
      return {
        access_token: `hs_${"a".repeat(32)}_${"b".repeat(64)}`,
        user: {
          credential_type: "cli",
          project: "demo",
          environment: "development",
        },
        expires_at: new Date(3600000).toISOString(),
        scope_id: "b".repeat(32),
      };
    },
  };
  const result = await deviceLogin(api, {
    show: () => {},
    launch: async () => {},
    now: () => time,
    wait: async (ms) => {
      waits.push(ms);
      time += ms;
    },
  });
  assert.deepEqual(waits, [5000, 5000, 10000]);
  assert.equal(result.origin, api.origin);
  assert.equal(result.scope_id, "b".repeat(32));
});
test("device authorization rejects cross-origin consent and denial without saving credentials", async () => {
  const options = {
    show: () => {},
    launch: async () => assert.fail("must not open unsafe browser URL"),
  };
  await assert.rejects(
    deviceLogin(
      {
        origin: "https://a.example",
        call: async () => ({
          ...challenge,
          verification_uri_complete:
            "https://evil.example/login/device?user_code=ABCD-EFGH",
        }),
      },
      options,
    ),
    /invalid browser/,
  );
  await assert.rejects(
    deviceLogin(
      {
        origin: "https://a.example",
        call: async (path) => {
          if (path.endsWith("start")) return challenge;
          throw new APIError(403, {
            error: { code: "access_denied", message: "Denied" },
          });
        },
      },
      { show: () => {}, launch: async () => {}, wait: async () => {} },
    ),
    /Denied/,
  );
});
test("expired device requests stop within the server deadline", async () => {
  let time = 0;
  await assert.rejects(
    deviceLogin(
      {
        origin: "https://a.example",
        call: async () => ({ ...challenge, expires_in: 5 }),
      },
      {
        show: () => {},
        launch: async () => {},
        now: () => time,
        wait: async (ms) => {
          time += ms;
        },
      },
    ),
    /expired/,
  );
});
