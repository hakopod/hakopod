import assert from 'node:assert/strict';
import { createServer } from 'node:http';
import { once } from 'node:events';
import test from 'node:test';
import { deploy, parseInputs } from '../deploy.mjs';

// Every API response in this file is a synthetic development fixture. These
// tests exercise the action's HTTP contract, not a running Hakopod cluster.
const APP_ID = 'a'.repeat(32);
const DEPLOYMENT_ID = 'b'.repeat(32);
const TOKEN = 'fixture-api-token-do-not-log';
const OLD_IMAGE = `ghcr.io/example/api@sha256:${'1'.repeat(64)}`;
const NEW_IMAGE = `ghcr.io/example/api@sha256:${'2'.repeat(64)}`;
const WORKER_IMAGE = `ghcr.io/example/worker@sha256:${'3'.repeat(64)}`;

function inputs(overrides = {}, mask = () => {}) {
  const values = {
    'api-url': 'https://hakopod.example',
    'api-token': TOKEN,
    'application-id': APP_ID,
    services: '["api"]',
    ...overrides,
  };
  return parseInputs(Object.fromEntries(Object.entries(values).map(([name, value]) => [
    `INPUT_${name.toUpperCase()}`, value,
  ])), mask);
}

function syntheticApplication() {
  return {
    id: APP_ID,
    project: 'example',
    environment: 'production',
    revision: 7,
    spec: {
      schema_version: 1,
      name: 'fixture-app',
      env: { SHARED: 'application-scope' },
      secrets: { SHARED_PASSWORD: { ref: 'shared-password' } },
      networks: { private: { internal: true } },
      volumes: { data: { size_gib: 10 } },
      domains: { 'app.example': 'api' },
      services: {
        api: {
          image: OLD_IMAGE,
          port: 8080,
          public: true,
          size: 'small',
          replicas: 2,
          networks: ['private'],
          env: { KEEP: 'unchanged', REMOVE: 'old', OVERRIDE: 'old' },
          secrets: {
            PASSWORD: { ref: 'old-password' },
            RETAIN: { provider: 'vault', path: 'team/app', key: 'token' },
            REMOVE_SECRET: { ref: 'retired-password' },
          },
          readiness: { path: '/ready' },
          command: ['/app/server'],
        },
        worker: {
          image: OLD_IMAGE,
          public: false,
          size: 'medium',
          replicas: 1,
          networks: ['private'],
          env: { KEEP: 'worker-value', REMOVE: 'old', OVERRIDE: 'old' },
          secrets: { WORKER_PASSWORD: { ref: 'worker-password' } },
        },
        database: {
          image: `postgres@sha256:${'4'.repeat(64)}`,
          public: false,
          size: 'medium',
          replicas: 1,
          networks: ['private'],
          mounts: [{ volume: 'data', path: '/var/lib/postgresql/data' }],
          env: { POSTGRES_USER: 'fixture' },
        },
      },
    },
  };
}

function syntheticDeployment(overrides = {}) {
  return { id: DEPLOYMENT_ID, application_id: APP_ID, revision: 8, status: 'queued', ...overrides };
}

function json(value, status = 200, headers = {}) {
  return new Response(JSON.stringify(value), {
    status,
    headers: { 'content-type': 'application/json', ...headers },
  });
}

function harness({ application = syntheticApplication(), onPlan, onSubmit, onPoll, onRecover } = {}) {
  const calls = [];
  const outputs = {};
  const logs = [];
  let elapsed = 0;
  const fetch = async (input, init = {}) => {
    const url = new URL(input);
    const call = {
      path: url.pathname,
      method: init.method ?? 'GET',
      headers: new Headers(init.headers),
      body: init.body ? JSON.parse(init.body) : undefined,
      redirect: init.redirect,
    };
    calls.push(call);
    if (call.path === `/api/v1/applications/${APP_ID}`) return json(application);
    if (call.path === '/api/v1/plan') {
      const plan = {
        application_id: APP_ID,
        expected_revision: application.revision,
        spec: call.body.spec,
        missing_secrets: [],
        changes: [],
        warnings: [],
      };
      return onPlan ? onPlan(call, plan) : json(plan);
    }
    if (call.path === '/api/v1/deployments') {
      return onSubmit ? onSubmit(call) : json(syntheticDeployment(), 202);
    }
    if (call.path === `/api/v1/deployments/${DEPLOYMENT_ID}`) {
      return onPoll ? onPoll(call) : json(syntheticDeployment({ status: 'succeeded' }));
    }
    if (call.path.startsWith('/api/v1/idempotency/')) {
      return onRecover ? onRecover(call) : json({ error: { code: 'not_found' } }, 404);
    }
    assert.fail(`Unexpected synthetic API request: ${call.method} ${call.path}`);
  };
  return {
    calls, outputs, logs,
    dependencies: {
      fetch,
      now: () => elapsed,
      sleep: async (ms) => { elapsed += ms; },
      output: (name, value) => { outputs[name] = String(value); },
      log: (message) => { logs.push(message); },
    },
  };
}

test('selected services preserve application state, merge env patches, and submit the canonical reviewed revision', async () => {
  const application = syntheticApplication();
  const before = structuredClone(application);
  let reviewedSpec;
  const h = harness({
    application,
    onPlan: (call, plan) => {
      reviewedSpec = structuredClone(plan.spec);
      // The server may normalize the specification during planning.
      reviewedSpec.services.api.update_strategy = 'rolling';
      return json({ ...plan, spec: reviewedSpec });
    },
  });
  const result = await deploy(inputs({
    image: NEW_IMAGE,
    services: JSON.stringify([
      { name: 'api', env: { OVERRIDE: 'service-value', EMPTY: '' }, secrets: { PASSWORD: 'new-password', REMOVE_SECRET: null } },
      { name: 'worker', image: WORKER_IMAGE },
    ]),
    env: JSON.stringify({ ADDED: 'common-value', REMOVE: null, OVERRIDE: 'common-value' }),
    workspace: 'c'.repeat(32),
  }), h.dependencies);

  const planned = h.calls.find((call) => call.path === '/api/v1/plan').body;
  const submitted = h.calls.find((call) => call.path === '/api/v1/deployments').body;
  assert.deepEqual(planned.services, ['api', 'worker']);
  assert.equal(planned.project, application.project);
  assert.equal(planned.environment, application.environment);
  assert.equal(planned.expected_revision, 7);
  assert.equal(planned.spec.services.api.image, NEW_IMAGE);
  assert.equal(planned.spec.services.worker.image, WORKER_IMAGE);
  assert.deepEqual(planned.spec.services.api.env, {
    KEEP: 'unchanged', ADDED: 'common-value', OVERRIDE: 'service-value', EMPTY: '',
  });
  assert.deepEqual(planned.spec.services.worker.env, {
    KEEP: 'worker-value', ADDED: 'common-value', OVERRIDE: 'common-value',
  });
  assert.deepEqual(planned.spec.services.api.secrets, {
    PASSWORD: { ref: 'new-password' }, RETAIN: before.spec.services.api.secrets.RETAIN,
  });
  assert.deepEqual(planned.spec.services.database, before.spec.services.database);
  for (const key of ['env', 'secrets', 'networks', 'volumes', 'domains']) {
    assert.deepEqual(planned.spec[key], before.spec[key]);
  }
  for (const key of ['replicas', 'size', 'port', 'readiness', 'command', 'networks']) {
    assert.deepEqual(planned.spec.services.api[key], before.spec.services.api[key]);
  }
  assert.equal(Object.hasOwn(submitted, 'services'), false);
  assert.deepEqual(submitted.spec, reviewedSpec);
  assert.equal(submitted.expected_revision, 7);
  assert.equal(submitted.project, application.project);
  assert.equal(submitted.environment, application.environment);
  assert.deepEqual(application, before);
  assert.equal(result.status, 'succeeded');
  for (const call of h.calls) {
    assert.equal(call.headers.get('authorization'), `Bearer ${TOKEN}`);
    assert.equal(call.headers.get('x-hakopod-workspace'), 'c'.repeat(32));
    assert.equal(call.redirect, 'error');
  }
  assert.equal(h.outputs['deployment-id'], DEPLOYMENT_ID);
  assert.equal(h.outputs['application-id'], APP_ID);
  assert.equal(h.outputs.revision, '8');
  assert.equal(h.outputs.status, 'succeeded');
  assert.match(h.outputs['idempotency-key'], /^[a-zA-Z0-9-]{8,128}$/);
  assert.equal(h.calls.find((call) => call.path === '/api/v1/deployments').headers.get('idempotency-key'), h.outputs['idempotency-key']);
});

test('string service selection supports environment-only changes without requiring an image', async () => {
  const h = harness();
  await deploy(inputs({ env: '{"RELEASE":"fixture-release"}', wait: 'false' }), h.dependencies);
  const spec = h.calls.find((call) => call.path === '/api/v1/plan').body.spec;
  assert.equal(spec.services.api.image, OLD_IMAGE);
  assert.equal(spec.services.api.env.RELEASE, 'fixture-release');
  assert.equal(spec.services.worker.env.RELEASE, undefined);
  assert.equal(h.outputs.status, 'queued');
  assert.equal(h.calls.some((call) => call.path === `/api/v1/deployments/${DEPLOYMENT_ID}`), false);
});

test('registers the API token with workflow masking', () => {
  const masked = [];
  inputs({ env: '{"PASSWORD":"common-sensitive-value"}', services: '[{"name":"api","env":{"TOKEN":"service-sensitive-value"}}]' }, (value) => masked.push(value));
  assert.ok(masked.includes(TOKEN));
});

test('normalizes supported API URL forms without changing the intended origin', async (t) => {
  for (const url of [
    'https://hakopod.example', 'https://hakopod.example/',
    'https://hakopod.example/api/v1', 'https://hakopod.example/api/v1/',
    'http://localhost:8080', 'http://127.0.0.1:8080', 'http://[::1]:8080',
  ]) {
    await t.test(url, async () => {
      const h = harness();
      await deploy(inputs({ 'api-url': url, wait: 'false' }), h.dependencies);
      assert.equal(h.calls[0].path, `/api/v1/applications/${APP_ID}`);
    });
  }
});

test('rejects unsafe or ambiguous API destinations and header inputs', () => {
  for (const url of [
    'http://hakopod.example', 'ftp://hakopod.example',
    'https://user:password@hakopod.example', 'https://hakopod.example/api',
    'https://hakopod.example/?token=secret', 'https://hakopod.example/#fragment',
    'https://hakopod.example/api/v1/../v1',
  ]) {
    assert.throws(() => inputs({ 'api-url': url }), undefined, url);
  }
  assert.throws(() => inputs({ 'api-token': 'token\r\nX-Injected: value' }));
  assert.throws(() => inputs({ workspace: 'workspace\r\nX-Injected: value' }));
  assert.throws(() => inputs({ 'application-id': '../other' }));
});

test('requires explicit credentials, application, and a bounded unique services array', () => {
  for (const field of ['api-url', 'api-token', 'application-id', 'services']) {
    assert.throws(() => inputs({ [field]: '' }), undefined, field);
  }
  for (const services of [
    '[]', '{}', 'null', '"api"', '[', '[""]', '["api","api"]',
    '["api",{"name":"api"}]', '[42]', '[{}]',
    '[{"name":"api","unknown":"typo"}]',
    JSON.stringify(Array.from({ length: 21 }, (_, index) => `service-${index}`)),
  ]) {
    assert.throws(() => inputs({ services }), undefined, services);
  }
});

test('rejects mutable images and invalid env or secret patches before network access', () => {
  for (const image of ['nginx:latest', 'ghcr.io/example/api:release', 'nginx@sha256:abc', `nginx@sha256:${'g'.repeat(64)}`]) {
    assert.throws(() => inputs({ image }), undefined, image);
    assert.throws(() => inputs({ services: JSON.stringify([{ name: 'api', image }]) }));
  }
  for (const env of ['[]', 'null', '{', '{"PORT":8080}', '{"ENABLED":true}', '{"INVALID-NAME":"value"}', '{"NESTED":{}}']) {
    assert.throws(() => inputs({ env }), undefined, env);
    assert.throws(() => inputs({ services: `[{"name":"api","env":${env}}]` }));
  }
  for (const secrets of [[], { PASSWORD: 123 }, { PASSWORD: { ref: 'name' } }, { PASSWORD: '' }, { 'INVALID-NAME': 'password' }]) {
    assert.throws(() => inputs({ services: JSON.stringify([{ name: 'api', secrets }]) }));
  }
});

test('env patches preserve unusual valid variable names without mutating object prototypes', async () => {
  const h = harness();
  await deploy(inputs({ env: '{"__proto__":"fixture-value","constructor":"fixture-constructor"}', wait: 'false' }), h.dependencies);
  const env = h.calls.find((call) => call.path === '/api/v1/plan').body.spec.services.api.env;
  assert.equal(Object.hasOwn(env, '__proto__'), true);
  assert.equal(env.__proto__, 'fixture-value');
  assert.equal(env.constructor, 'fixture-constructor');
  assert.equal(Object.getPrototypeOf(env), Object.prototype);
  assert.equal({}.polluted, undefined);
});

test('limits input bytes and variable count before contacting the API', () => {
  assert.throws(() => inputs({ env: JSON.stringify({ LARGE: 'x'.repeat(4097) }) }));
  assert.throws(() => inputs({ env: JSON.stringify(Object.fromEntries(Array.from({ length: 129 }, (_, index) => [`VAR_${index}`, 'value']))) }));
  assert.throws(() => inputs({ services: `["api"${' '.repeat(256 * 1024)}]` }));
});

test('validates wait and bounded polling inputs', () => {
  for (const wait of ['yes', '1', 'FALSE-ish']) assert.throws(() => inputs({ wait }));
  for (const timeout of ['0', '3601', '-1', '1.5', 'NaN']) assert.throws(() => inputs({ timeout }));
  for (const interval of ['0', '61', '-1', '1.5', 'NaN']) assert.throws(() => inputs({ 'poll-interval': interval }));
});

test('an unknown service never reaches planning or deployment', async () => {
  const h = harness();
  await assert.rejects(deploy(inputs({ services: '["does-not-exist"]' }), h.dependencies));
  assert.deepEqual(h.calls.map((call) => call.method), ['GET']);
});

test('rejects stale plan revision, wrong application, and missing secrets before submission', async (t) => {
  for (const [name, patch] of [
    ['stale revision', { expected_revision: 8 }],
    ['wrong application', { application_id: 'd'.repeat(32) }],
    ['missing application', { application_id: '' }],
    ['missing secrets', { missing_secrets: ['required-password'] }],
  ]) {
    await t.test(name, async () => {
      const h = harness({ onPlan: (call, plan) => json({ ...plan, ...patch }) });
      await assert.rejects(deploy(inputs(), h.dependencies));
      assert.equal(h.calls.some((call) => call.path === '/api/v1/deployments'), false);
    });
  }
});

test('rejects fetched application identity mismatch before planning', async () => {
  const h = harness({ application: { ...syntheticApplication(), id: 'e'.repeat(32) } });
  await assert.rejects(deploy(inputs(), h.dependencies));
  assert.equal(h.calls.length, 1);
});

test('a concurrent revision conflict fails without repeating deployment submission', async () => {
  const h = harness({ onSubmit: () => json({ error: { code: 'revision_conflict', message: 'fixture-private-api-error' } }, 409) });
  await assert.rejects(deploy(inputs(), h.dependencies), (error) => {
    assert.doesNotMatch(error.message, /fixture-private-api-error/);
    return true;
  });
  assert.equal(h.calls.filter((call) => call.path === '/api/v1/deployments').length, 1);
});

test('retries transient reads within the deadline without resubmitting the deployment', async () => {
  let polls = 0;
  const h = harness({
    onPoll: () => {
      polls++;
      if (polls === 1) return json({ error: { message: TOKEN } }, 503);
      if (polls === 2) return json({ error: { message: TOKEN } }, 429);
      return json(syntheticDeployment({ status: 'succeeded' }));
    },
  });
  const result = await deploy(inputs(), h.dependencies);
  assert.equal(result.status, 'succeeded');
  assert.equal(polls, 3);
  assert.equal(h.calls.filter((call) => call.path === '/api/v1/deployments').length, 1);
});

test('recovers an ambiguously accepted deployment by key without repeating POST', async (t) => {
  for (const failure of ['network', 'server error', 'malformed success']) {
    await t.test(failure, async () => {
      let submittedKey;
      const h = harness({
        onSubmit: (call) => {
          submittedKey = call.headers.get('idempotency-key');
          if (failure === 'network') throw new TypeError(`Synthetic network failure ${TOKEN}`);
          if (failure === 'server error') return json({ error: { message: TOKEN } }, 503);
          return new Response('{', { status: 202, headers: { 'content-type': 'application/json' } });
        },
        onRecover: (call) => {
          assert.equal(call.path, `/api/v1/idempotency/${submittedKey}`);
          return json(syntheticDeployment({ status: 'running' }));
        },
      });
      const result = await deploy(inputs(), h.dependencies);
      assert.equal(result.status, 'succeeded');
      assert.equal(h.calls.filter((call) => call.path === '/api/v1/deployments').length, 1);
      assert.equal(h.calls.filter((call) => call.path.startsWith('/api/v1/idempotency/')).length, 1);
      assert.doesNotMatch(h.logs.join('\n'), new RegExp(TOKEN));
    });
  }
});

test('an ambiguous submission that cannot be recovered fails safely and retains the retry key', async () => {
  const h = harness({
    onSubmit: () => { throw new TypeError(`Synthetic socket failure ${TOKEN}`); },
    onRecover: () => json({ error: { message: 'fixture-private-error' } }, 404),
  });
  await assert.rejects(deploy(inputs({ timeout: '2', 'poll-interval': '1' }), h.dependencies), (error) => {
    assert.doesNotMatch(error.message, /fixture-private-error|fixture-api-token-do-not-log/);
    return true;
  });
  assert.equal(h.calls.filter((call) => call.path === '/api/v1/deployments').length, 1);
  assert.ok(h.outputs['idempotency-key']);
});

test('a recovered deployment must belong to the requested application', async () => {
  const h = harness({
    onSubmit: () => { throw new TypeError('Synthetic lost response'); },
    onRecover: () => json(syntheticDeployment({ application_id: 'f'.repeat(32) })),
  });
  await assert.rejects(deploy(inputs(), h.dependencies));
  assert.equal(h.calls.filter((call) => call.path === '/api/v1/deployments').length, 1);
  assert.equal(h.calls.some((call) => call.path === `/api/v1/deployments/${DEPLOYMENT_ID}`), false);
});

test('failed, cancelled, and superseded deployments fail the action without printing API error text', async (t) => {
  for (const status of ['failed', 'cancelled', 'superseded']) {
    await t.test(status, async () => {
      const h = harness({ onPoll: () => json(syntheticDeployment({ status, error: `fixture-sensitive-deployment-error ${TOKEN}` })) });
      await assert.rejects(deploy(inputs(), h.dependencies), (error) => {
        assert.doesNotMatch(error.message, /fixture-sensitive-deployment-error|fixture-api-token-do-not-log/);
        return true;
      });
      assert.equal(h.outputs.status, status);
    });
  }
});

test('polling validates deployment identity and revision', async (t) => {
  for (const patch of [{ application_id: 'f'.repeat(32) }, { id: 'f'.repeat(32) }, { revision: 99 }]) {
    await t.test(JSON.stringify(patch), async () => {
      const h = harness({ onPoll: () => json(syntheticDeployment({ ...patch, status: 'succeeded' })) });
      await assert.rejects(deploy(inputs(), h.dependencies));
    });
  }
});

test('polling has a deadline and does not cancel an accepted deployment on timeout', async () => {
  const h = harness({ onPoll: () => json(syntheticDeployment({ status: 'running' })) });
  await assert.rejects(deploy(inputs({ timeout: '2', 'poll-interval': '1' }), h.dependencies), /timed out|timeout|deadline/i);
  assert.equal(h.outputs['deployment-id'], DEPLOYMENT_ID);
  assert.equal(h.calls.some((call) => /cancel/.test(call.path)), false);
  assert.ok(h.calls.length < 10, 'poll count is bounded by the configured deadline');
});

test('rejects malformed and oversized API bodies without including their contents', async (t) => {
  for (const [name, response, expectedCalls] of [
    ['invalid JSON', () => new Response(`not-json ${TOKEN}`, { status: 200 }), 3],
    ['oversized JSON', () => json({ padding: 'x'.repeat(2 * 1024 * 1024) }), 3],
    ['wrong shape', () => json(null), 3],
    ['API denial', () => json({ error: { message: `private auth detail ${TOKEN}` } }, 403), 1],
    ['redirect', () => new Response(null, { status: 302, headers: { location: 'https://other.example' } }), 1],
  ]) {
    await t.test(name, async () => {
      let calls = 0;
      await assert.rejects(deploy(inputs(), {
        fetch: async () => { calls++; return response(); },
        sleep: async () => {},
      }), (error) => {
        assert.doesNotMatch(error.message, /fixture-api-token-do-not-log|private auth detail/);
        return true;
      });
      assert.equal(calls, expectedCalls);
    });
  }
});

test('runs the complete HTTP workflow against an explicitly synthetic loopback server', async (t) => {
  const received = [];
  let acceptedSpec;
  let key;
  const server = createServer(async (request, response) => {
    try {
      const chunks = [];
      for await (const chunk of request) chunks.push(chunk);
      const body = chunks.length ? JSON.parse(Buffer.concat(chunks).toString()) : undefined;
      received.push({ method: request.method, path: request.url, body });
      assert.equal(request.headers.authorization, `Bearer ${TOKEN}`);
      response.setHeader('content-type', 'application/json');
      if (request.url === `/api/v1/applications/${APP_ID}`) {
        response.end(JSON.stringify(syntheticApplication()));
      } else if (request.url === '/api/v1/plan') {
        acceptedSpec = body.spec;
        assert.deepEqual(body.services, ['api']);
        response.end(JSON.stringify({ application_id: APP_ID, expected_revision: 7, spec: acceptedSpec, missing_secrets: [] }));
      } else if (request.url === '/api/v1/deployments') {
        assert.deepEqual(body.spec, acceptedSpec);
        assert.equal(body.expected_revision, 7);
        assert.equal(Object.hasOwn(body, 'services'), false);
        key = request.headers['idempotency-key'];
        assert.ok(key);
        response.statusCode = 202;
        response.end(JSON.stringify(syntheticDeployment()));
      } else if (request.url === `/api/v1/deployments/${DEPLOYMENT_ID}`) {
        response.end(JSON.stringify(syntheticDeployment({ status: 'succeeded' })));
      } else {
        response.statusCode = 404;
        response.end('{}');
      }
    } catch (error) {
      response.statusCode = 500;
      response.end(JSON.stringify({ fixture_error: error.message }));
    }
  });
  server.listen(0, '127.0.0.1');
  await once(server, 'listening');
  t.after(() => new Promise((resolve, reject) => {
    server.closeAllConnections();
    server.close((error) => error ? reject(error) : resolve());
  }));
  const result = await deploy(inputs({
    'api-url': `http://127.0.0.1:${server.address().port}/api/v1`,
    image: NEW_IMAGE,
    env: '{"RELEASE":"synthetic-http-fixture"}',
  }), { sleep: async () => {} });
  assert.equal(result.status, 'succeeded');
  assert.deepEqual(received.map(({ method, path }) => [method, path]), [
    ['GET', `/api/v1/applications/${APP_ID}`],
    ['POST', '/api/v1/plan'],
    ['POST', '/api/v1/deployments'],
    ['GET', `/api/v1/deployments/${DEPLOYMENT_ID}`],
  ]);
  assert.equal(acceptedSpec.services.api.image, NEW_IMAGE);
  assert.equal(acceptedSpec.services.api.env.RELEASE, 'synthetic-http-fixture');
});
