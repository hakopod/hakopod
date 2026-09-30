import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { once } from 'node:events';
import { mkdtemp, readFile, rm, writeFile } from 'node:fs/promises';
import { createServer } from 'node:http';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import test from 'node:test';
import { fileURLToPath } from 'node:url';

// These entrypoint tests use synthetic HTTP responses, never a real cluster.
const APP_ID = 'a'.repeat(32);
const DEPLOYMENT_ID = 'b'.repeat(32);
const TOKEN = 'synthetic-entrypoint-token';
const IMAGE = `ghcr.io/example/api@sha256:${'2'.repeat(64)}`;
const entrypoint = fileURLToPath(new URL('../index.mjs', import.meta.url));

async function syntheticServer(t, handler) {
  const requests = [];
  const errors = [];
  const server = createServer(async (request, response) => {
    try {
      const chunks = [];
      for await (const chunk of request) chunks.push(chunk);
      const call = {
        method: request.method,
        path: request.url,
        headers: request.headers,
        body: chunks.length ? JSON.parse(Buffer.concat(chunks).toString()) : undefined,
      };
      requests.push(call);
      const result = handler(call);
      response.writeHead(result.status ?? 200, { 'content-type': 'application/json' });
      response.end(JSON.stringify(result.body));
    } catch (error) {
      errors.push(error);
      response.writeHead(500, { 'content-type': 'application/json' });
      response.end('{}');
    }
  });
  server.listen(0, '127.0.0.1');
  await once(server, 'listening');
  t.after(() => new Promise((resolve, reject) => {
    server.closeAllConnections();
    server.close(error => error ? reject(error) : resolve());
  }));
  return { url: `http://127.0.0.1:${server.address().port}`, requests, errors };
}

async function runEntrypoint(t, apiUrl, overrides = {}) {
  const directory = await mkdtemp(join(tmpdir(), 'hakopod-action-fixture-'));
  t.after(() => rm(directory, { recursive: true, force: true }));
  const outputFile = join(directory, 'github-output');
  await writeFile(outputFile, '');
  const child = spawn(process.execPath, [entrypoint], {
    stdio: ['ignore', 'pipe', 'pipe'],
    env: {
      'INPUT_API-URL': apiUrl,
      'INPUT_API-TOKEN': TOKEN,
      'INPUT_APPLICATION-ID': APP_ID,
      INPUT_SERVICES: '["api"]',
      INPUT_IMAGE: IMAGE,
      INPUT_WAIT: 'true',
      INPUT_TIMEOUT: '5',
      'INPUT_POLL-INTERVAL': '1',
      GITHUB_OUTPUT: outputFile,
      ...overrides,
    },
  });
  let stdout = '';
  let stderr = '';
  child.stdout.setEncoding('utf8');
  child.stderr.setEncoding('utf8');
  child.stdout.on('data', value => { stdout += value; });
  child.stderr.on('data', value => { stderr += value; });
  const timeout = setTimeout(() => child.kill('SIGKILL'), 10_000);
  t.after(() => { clearTimeout(timeout); if (child.exitCode === null) child.kill('SIGKILL'); });
  const [code, signal] = await once(child, 'close');
  clearTimeout(timeout);
  return { code, signal, stdout, stderr, output: await readFile(outputFile, 'utf8') };
}

test('the actual action entrypoint deploys over HTTP and writes GitHub output values', async (t) => {
  const appSpec = {
    schema_version: 1,
    name: 'fixture-app',
    services: { api: { image: IMAGE, env: { KEEP: 'unchanged' }, replicas: 2 } },
  };
  const deployment = { id: DEPLOYMENT_ID, application_id: APP_ID, revision: 8, status: 'queued' };
  let plannedSpec;
  const fixture = await syntheticServer(t, call => {
    if (call.path === `/api/v1/applications/${APP_ID}`) {
      return { body: { id: APP_ID, project: 'example', environment: 'production', revision: 7, spec: appSpec } };
    }
    if (call.path === '/api/v1/plan') {
      plannedSpec = call.body.spec;
      return { body: { application_id: APP_ID, expected_revision: 7, spec: plannedSpec, missing_secrets: [] } };
    }
    if (call.path === '/api/v1/deployments') return { status: 202, body: deployment };
    if (call.path === `/api/v1/deployments/${DEPLOYMENT_ID}`) {
      return { body: { ...deployment, status: 'succeeded' } };
    }
    throw new Error(`Unexpected synthetic request: ${call.path}`);
  });
  const result = await runEntrypoint(t, fixture.url, { INPUT_ENV: '{"RELEASE":"synthetic-cli-fixture"}' });
  assert.equal(result.code, 0, result.stdout);
  assert.equal(result.signal, null);
  assert.equal(result.stderr, '');
  assert.deepEqual(fixture.errors, []);
  assert.deepEqual(fixture.requests.map(({ method, path }) => [method, path]), [
    ['GET', `/api/v1/applications/${APP_ID}`],
    ['POST', '/api/v1/plan'],
    ['POST', '/api/v1/deployments'],
    ['GET', `/api/v1/deployments/${DEPLOYMENT_ID}`],
  ]);
  assert.ok(fixture.requests.every(call => call.headers.authorization === `Bearer ${TOKEN}`));
  assert.deepEqual(fixture.requests[1].body.services, ['api']);
  assert.deepEqual(fixture.requests[2].body.spec, plannedSpec);
  assert.equal(plannedSpec.services.api.env.RELEASE, 'synthetic-cli-fixture');
  assert.equal(Object.hasOwn(fixture.requests[2].body, 'services'), false);
  const output = Object.fromEntries(result.output.trim().split('\n').map(line => line.split('=')));
  assert.deepEqual(Object.keys(output).sort(), ['application-id', 'deployment-id', 'idempotency-key', 'revision', 'status']);
  assert.equal(output['application-id'], APP_ID);
  assert.equal(output['deployment-id'], DEPLOYMENT_ID);
  assert.equal(output.revision, '8');
  assert.equal(output.status, 'succeeded');
  assert.equal(output['idempotency-key'], fixture.requests[2].headers['idempotency-key']);
  assert.match(output['idempotency-key'], /^[0-9a-f-]{36}$/);
  assert.equal(result.stdout.split('\n').filter(line => line === `::add-mask::${TOKEN}`).length, 1);
  assert.equal(result.stdout.split(TOKEN).length - 1, 1, 'the token only appears in its mask registration');
  assert.doesNotMatch(result.output, new RegExp(TOKEN));
  assert.match(result.stdout, new RegExp(`Deployment ${DEPLOYMENT_ID} succeeded\\.`));
});

test('the entrypoint rejects API errors without emitting injected workflow commands or raw response text', async (t) => {
  const injection = `synthetic-private-error ${TOKEN}\n::warning::synthetic-command-injection\n::set-output name=compromised::yes`;
  const fixture = await syntheticServer(t, () => ({ status: 403, body: { error: { message: injection } } }));
  const result = await runEntrypoint(t, fixture.url);
  assert.equal(result.code, 1);
  assert.equal(result.signal, null);
  assert.equal(result.stderr, '');
  assert.equal(result.output, '');
  assert.equal(fixture.requests.length, 1);
  assert.deepEqual(fixture.errors, []);
  assert.equal(result.stdout.split('\n').filter(line => line === `::add-mask::${TOKEN}`).length, 1);
  assert.equal(result.stdout.split(TOKEN).length - 1, 1);
  assert.match(result.stdout, /^::error::Application fetch failed \(HTTP 403\)\./m);
  assert.doesNotMatch(result.stdout, /synthetic-private-error|synthetic-command-injection|::warning::|::set-output|compromised/);
  assert.equal(result.stdout.split('\n').filter(line => line.startsWith('::')).length, 2);
});
