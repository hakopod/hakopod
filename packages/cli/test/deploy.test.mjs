import test from "node:test";
import assert from "node:assert/strict";
import { mkdtemp, writeFile, readFile, mkdir, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { fileURLToPath } from "node:url";
import { createServer } from "node:http";
import { execute, key, writePrivate } from "../src/core.mjs";

// Explicitly synthetic Git/provider/API adapters exercise the real npm executable.
// They are contract/retry tests, not proof of a live Kubernetes deployment.
async function fixture(t, { dropRunResponse = false, approval = false, missingSecrets = [] } = {}) {
  const dir = await mkdtemp(join(tmpdir(), "hakopod-cli-flow-"));
  t.after(() => rm(dir, { recursive: true, force: true }));
  const home = join(dir, "credentials");
  const bin = join(dir, "bin");
  await mkdir(bin);
  const initial = "1".repeat(40),
    installed = "2".repeat(40),
    build = "b".repeat(32),
    run = "c".repeat(32),
    deployment = "d".repeat(32);
  await writeFile(join(dir, "commit"), initial);
  await writeFile(
    join(bin, "git"),
    `#!/usr/bin/env node
import {readFileSync,writeFileSync} from 'node:fs';
const args=process.argv.slice(2),root=process.env.FIXTURE_ROOT;
const commit=()=>readFileSync(root+'/commit','utf8');
if(args[0]==='rev-parse')console.log(args[1]==='--show-toplevel'?root:commit());
else if(args[0]==='remote')console.log('git@github.com:fixture/app.git');
else if(args[0]==='symbolic-ref')console.log('main');
else if(args[0]==='ls-remote')console.log(commit()+'\\trefs/heads/main');
else if(args[0]==='merge')writeFileSync(root+'/commit',args[2]);
else if(!['status','fetch'].includes(args[0]))process.exit(1);
`,
    { mode: 0o700 },
  );
  const calls = [];
  let revision = 0,
    created = false,
    acceptedKey = "",
    dropped = false;
  const config = () => ({
    id: build,
    name: "fixture-app",
    project: "demo",
    environment: "development",
    provider: "github",
    repository: "fixture/app",
    branch: "main",
    context_path: ".",
    revision: 1,
    installed_revision: revision,
  });
  const server = createServer(async (req, res) => {
    let raw = "";
    for await (const chunk of req) raw += chunk;
    const body = raw ? JSON.parse(raw) : undefined;
    calls.push({
      method: req.method,
      path: req.url,
      body,
      key: req.headers["idempotency-key"],
    });
    assert.equal(req.headers.authorization, "Bearer hs_fixture");
    let data;
    const path = req.url.split("?")[0];
    if (path === "/api/v1/me")
      data = {
        credential_type: "cli",
        project: "demo",
        environment: "development",
      };
    else if (path === "/api/v1/git/connections")
      data = {
        items: [
          {
            id: "fixture",
            name: "Fixture Git",
            provider: "github",
            enabled: true,
            capabilities: { builds: true },
          },
        ],
      };
    else if (path === "/api/v1/builds/detect")
      data = {
        commit_sha: initial,
        mode: "framework",
        framework: {
          framework: "node",
          runtime: "node",
          package_manager: "npm",
          install_command: "npm ci",
          build_command: "npm run build",
          start_command: "npm start",
          port: 3000,
        },
        warnings: [],
      };
    else if (path === "/api/v1/builds" && req.method === "GET")
      data = { items: created ? [config()] : [] };
    else if (path === "/api/v1/builds" && req.method === "POST") {
      created = true;
      data = config();
    } else if (path === `/api/v1/builds/${build}`) data = config();
    else if (path.endsWith("/preview"))
      data = {
        workflow_path: ".github/workflows/hakopod.yml",
        workflow: "name: fixture build",
        requirements: ["GitHub Actions enabled"],
      };
    else if (path.endsWith("/install")) {
      revision = 1;
      await writeFile(join(dir, "commit"), installed);
      data = {
        config: config(),
        commit_sha: installed,
        workflow_branch: "main",
      };
    } else if (path.endsWith("/run")) {
      assert.equal(body.commit, installed);
      if (acceptedKey)
        assert.equal(req.headers["idempotency-key"], acceptedKey);
      acceptedKey = req.headers["idempotency-key"];
      if (dropRunResponse && !dropped) {
        dropped = true;
        req.socket.destroy();
        return;
      }
      data = { id: run };
    } else if (path === `/api/v1/builds/${build}/runs/${run}`)
      data = {
        id: run,
        status: "completed",
        conclusion: "success",
        image: "registry/fixture@sha256:" + "e".repeat(64),
        deployment_id: "",
      };
    else if (path.endsWith("/plan"))
      data = {
        expected_revision: 0,
        expected_config_revision: 1,
        missing_secrets: missingSecrets,
        changes: ["Create web service"],
        warnings: [],
      };
    else if (path.endsWith("/deploy")) {
      if (approval) {
        res.writeHead(409, { "Content-Type": "application/json" });
        res.end(
          JSON.stringify({
            error: { code: "approval_required", message: "Review required" },
          }),
        );
        return;
      }
      data = { id: deployment };
    } else if (path === `/api/v1/deployments/${deployment}`)
      data = { status: "succeeded", application_id: "a".repeat(32) };
    else {
      res.writeHead(404, { "Content-Type": "application/json" });
      res.end("{}");
      return;
    }
    res.writeHead(200, { "Content-Type": "application/json" });
    res.end(JSON.stringify(data));
  });
  await new Promise((resolve) => server.listen(0, "127.0.0.1", resolve));
  t.after(() => new Promise((resolve) => server.close(resolve)));
  const target = `http://127.0.0.1:${server.address().port}`;
  await writePrivate(join(home, `profile-${key(target)}.json`), {
    origin: target,
    token: "hs_fixture",
    expires_at: new Date(Date.now() + 3600000).toISOString(),
    project: "demo",
    environment: "development",
  });
  const command = async () =>
    execute(
      process.execPath,
      [
        fileURLToPath(new URL("../src/main.mjs", import.meta.url)),
        "deploy",
        "--url",
        target,
        "--name",
        "fixture-app",
        "--yes",
        "--no-browser",
      ],
      {
        cwd: dir,
        env: {
          ...process.env,
          PATH: `${bin}:${process.env.PATH}`,
          HAKOPOD_CLI_HOME: home,
          FIXTURE_ROOT: dir,
        },
        timeout: 20000,
      },
    );
  return { command, calls };
}
test("packaged executable reviews, installs, builds and waits for a successful release", async (t) => {
  const f = await fixture(t);
  const result = await f.command();
  assert.match(result.stdout, /Workflow ready/);
  assert.match(result.stdout, /Deployment succeeded/);
  assert.equal(f.calls.filter((c) => c.path.endsWith("/install")).length, 1);
  assert.ok(
    f.calls.find((c) => c.path.endsWith("/deploy")).body
      .expected_config_revision === 1,
  );
});
test("retry after a lost build response reuses the saved idempotency key and config", async (t) => {
  const f = await fixture(t, { dropRunResponse: true });
  await assert.rejects(f.command(), (error) => {
    assert.doesNotMatch(error.stdout, /Deployment succeeded/);
    return true;
  });
  const result = await f.command();
  assert.match(result.stdout, /Deployment succeeded/);
  const calls = f.calls.filter((c) => c.path.endsWith("/run"));
  assert.equal(calls.length, 2);
  assert.equal(calls[0].key, calls[1].key);
  assert.equal(
    f.calls.filter((c) => c.path === "/api/v1/builds" && c.method === "POST")
      .length,
    1,
  );
});
test("Cloud approval reports pending and never claims a successful deployment", async (t) => {
  const f = await fixture(t, { approval: true });
  await assert.rejects(f.command(), (error) => {
    assert.match(error.stdout, /awaits a Cloud Team review/);
    assert.doesNotMatch(error.stdout, /Deployment succeeded/);
    return true;
  });
  assert.equal(
    f.calls.filter((c) => c.path.startsWith("/api/v1/deployments/")).length,
    0,
  );
});

test("noninteractive missing secrets stop before deployment, even with --yes", async (t) => {
  const f = await fixture(t, { missingSecrets: ["database-password"] });
  await assert.rejects(f.command(), (error) => {
    assert.match(error.stdout, /Missing application secrets: database-password/);
    return true;
  });
  assert.equal(f.calls.filter(c => c.path.endsWith("/deploy")).length, 0);
});
