#!/usr/bin/env node
import { createServer } from 'node:http';

// Synthetic API for the GitHub runner smoke test. It checks action inputs and
// simulates success; it does not deploy to or verify a Kubernetes cluster.
const APP_ID = 'synthetic-runner-application';
const DEPLOYMENT_ID = 'synthetic-runner-deployment';
const TOKEN = 'synthetic-runner-token';
const IMAGE = `ghcr.io/example/api@sha256:${'1'.repeat(64)}`;
const deployment = { id: DEPLOYMENT_ID, application_id: APP_ID, revision: 2, status: 'succeeded' };
const spec = {
  schema_version: 1,
  name: 'synthetic-runner',
  services: { api: { image: `ghcr.io/example/api@sha256:${'0'.repeat(64)}`, replicas: 1 } },
};
let plannedSpec;
let acceptedKey;

function reply(response, status, body) {
  response.writeHead(status, { 'content-type': 'application/json' });
  response.end(JSON.stringify(body));
}

const server = createServer(async (request, response) => {
  if (request.method === 'GET' && request.url === '/healthz') return reply(response, 200, { fixture: 'synthetic' });
  if (request.headers.authorization !== `Bearer ${TOKEN}`) return reply(response, 401, { error: { code: 'fixture_token_required' } });
  try {
    let length = 0;
    const chunks = [];
    for await (const chunk of request) {
      length += chunk.length;
      if (length > 256 * 1024) return reply(response, 413, { error: { code: 'fixture_body_too_large' } });
      chunks.push(chunk);
    }
    const body = chunks.length ? JSON.parse(Buffer.concat(chunks).toString('utf8')) : undefined;
    if (request.method === 'GET' && request.url === `/api/v1/applications/${APP_ID}`) {
      return reply(response, 200, { id: APP_ID, project: 'fixture', environment: 'test', revision: 1, spec });
    }
    if (request.method === 'POST' && request.url === '/api/v1/plan') {
      if (body.project !== 'fixture' || body.environment !== 'test' || body.expected_revision !== 1 ||
        JSON.stringify(body.services) !== '["api"]' || body.spec?.services?.api?.image !== IMAGE ||
        body.spec.services.api.env?.RELEASE !== 'synthetic-runner' || body.spec.services.api.env?.SHARED !== 'synthetic-runner') {
        return reply(response, 400, { error: { code: 'fixture_inputs_did_not_match' } });
      }
      plannedSpec = body.spec;
      return reply(response, 200, { application_id: APP_ID, expected_revision: 1, spec: plannedSpec, missing_secrets: [] });
    }
    if (request.method === 'POST' && request.url === '/api/v1/deployments') {
      const key = request.headers['idempotency-key'];
      if (!plannedSpec || body.project !== 'fixture' || body.environment !== 'test' || body.expected_revision !== 1 ||
        body.services !== undefined || JSON.stringify(body.spec) !== JSON.stringify(plannedSpec) ||
        typeof key !== 'string' || !/^[a-f0-9-]{36}$/.test(key)) {
        return reply(response, 400, { error: { code: 'fixture_submission_did_not_match_plan' } });
      }
      acceptedKey = key;
      return reply(response, 202, deployment);
    }
    if (request.method === 'GET' && acceptedKey &&
      (request.url === `/api/v1/deployments/${DEPLOYMENT_ID}` || request.url === `/api/v1/idempotency/${acceptedKey}`)) {
      return reply(response, 200, deployment);
    }
    reply(response, 404, { error: { code: 'fixture_route_not_found' } });
  } catch {
    reply(response, 400, { error: { code: 'fixture_invalid_request' } });
  }
});
server.maxConnections = 16;
server.requestTimeout = 5000;
server.headersTimeout = 5000;

const rawPort = process.argv.length === 2 ? '18777' : process.argv.length === 4 && process.argv[2] === '--port' ? process.argv[3] : '';
if (!/^\d+$/.test(rawPort) || Number(rawPort) > 65535) {
  process.stderr.write('Usage: node testdata/runner.mjs [--port 18777]\n');
  process.exitCode = 1;
} else {
  server.on('error', () => { process.stderr.write('The synthetic API could not listen on loopback.\n'); process.exitCode = 1; });
  server.listen(Number(rawPort), '127.0.0.1', () => {
    process.stdout.write(`Synthetic deployment API listening on http://127.0.0.1:${server.address().port}\n`);
  });
  process.on('SIGTERM', () => { server.closeAllConnections(); server.close(); });
}
