import { forwardAutomationAPI } from './automation-api.ts'
import assert from 'node:assert/strict'
import test from 'node:test'
import { proxy } from './api-proxy.ts'

const app = '0ed1ea04daed35bba5a60f60fd029a7c'
function request(path: string, method = 'GET', body?: string, headers = {}) {
  return proxy({
    request: new Request('https://dashboard.example/api/v1/' + path, {
      method,
      headers: { Authorization: 'Bearer hp_fixture', ...headers },
      body,
    }),
    params: { _splat: 'v1/' + path.split('?')[0] },
  })
}

test('CI fetches an application with its own key, without a browser session', async (t) => {
  t.mock.method(globalThis, 'fetch', async (url: unknown, init?: RequestInit) => {
    assert.equal(new URL(String(url)).pathname, '/api/v1/applications/' + app)
    const headers = new Headers(init?.headers)
    assert.equal(headers.get('Authorization'), 'Bearer hp_fixture')
    for (const name of ['Cookie', 'X-Hakopod-Workspace', 'X-Forwarded-For'])
      assert.equal(headers.has(name), false)
    assert.equal(init?.redirect, 'error')
    return Response.json(
      {
        id: app,
        spec: { services: { api: {}, setup: {}, 'celery-beat': {}, 'celery-worker': {} } },
      },
      { headers: { 'Set-Cookie': 'must-not-forward' } },
    )
  })
  const response = await request('applications/' + app, 'GET', undefined, {
    Cookie: 'hakopod_session=browser',
    'X-Hakopod-Workspace': 'another-workspace',
    'X-Forwarded-For': '127.0.0.1',
  })
  assert.equal(response.status, 200)
  assert.equal(response.headers.has('Set-Cookie'), false)
  assert.deepEqual(Object.keys((await response.json()).spec.services), [
    'api',
    'setup',
    'celery-beat',
    'celery-worker',
  ])
})

test('CI forwards exact reviewed group payloads, idempotency and scope queries', async (t) => {
  const payload = JSON.stringify({
    project: 'demo',
    environment: 'production',
    expected_revision: 14,
    services: ['setup', 'celery-beat', 'celery-worker', 'api'],
    spec: {
      name: 'fixture',
      services: { api: { image: 'ghcr.io/example/api@sha256:' + 'a'.repeat(64) } },
    },
  })
  t.mock.method(globalThis, 'fetch', async (url: unknown, init?: RequestInit) => {
    assert.equal(new URL(String(url)).search, '?project=demo&environment=production')
    const headers = new Headers(init?.headers)
    assert.equal(headers.get('Idempotency-Key'), 'ci-fixture-12345678')
    assert.equal(headers.get('Content-Type'), 'application/json')
    assert.equal(new TextDecoder().decode(init?.body as Uint8Array), payload)
    assert.equal(init?.method, 'POST')
    assert.ok(init?.signal)
    return Response.json({ id: 'release-fixture' }, { status: 202 })
  })
  for (const path of ['plan', 'deployments']) {
    const response = await request(path + '?project=demo&environment=production', 'POST', payload, {
      'Content-Type': 'application/json',
      'Idempotency-Key': 'ci-fixture-12345678',
    })
    assert.equal(response.status, 202)
  }
})

test('CI forwards canonical authorization, conflict and rate-limit errors intact', async (t) => {
  for (const status of [401, 403, 409, 429, 503]) {
    const body = { error: { code: 'fixture', message: 'fixture error' } }
    const mock = t.mock.method(globalThis, 'fetch', async () =>
      Response.json(body, { status, headers: { 'Retry-After': '30' } }),
    )
    const response = await request('deployments/release-fixture')
    assert.equal(response.status, status)
    assert.equal(response.headers.get('Retry-After'), '30')
    assert.deepEqual(await response.json(), body)
    mock.mock.restore()
  }
})

test('CI denies cookies, browser tokens, unsupported methods/routes and oversized input', async (t) => {
  const fetch = t.mock.method(globalThis, 'fetch', async () => {
    throw new Error('must not forward')
  })
  for (const authorization of ['', 'Bearer browser-token']) {
    assert.equal(
      (
        await request('applications/' + app, 'GET', undefined, {
          Authorization: authorization,
          Cookie: 'hakopod_session=browser',
        })
      ).status,
      401,
    )
  }
  assert.equal((await request('applications/' + app, 'DELETE')).status, 405)
  assert.equal((await request('keys')).status, 404)
  assert.equal((await request('cloud/workspaces')).status, 404)
  assert.equal((await request('deployments', 'POST', 'x'.repeat(1024 * 1024 + 1))).status, 413)
  assert.equal(fetch.mock.callCount(), 0)
})

test('CI lookup routes retain response data and transport failure stays explicit', async (t) => {
  for (const path of [
    'applications',
    'applications/' + app + '/provenance',
    'idempotency/ci-12345678',
    'openapi.json',
  ]) {
    const mock = t.mock.method(globalThis, 'fetch', async (url: unknown) => {
      assert.equal(new URL(String(url)).pathname, '/api/v1/' + path)
      return Response.json({ fixture: true })
    })
    assert.equal((await request(path)).status, 200)
    mock.mock.restore()
  }
  t.mock.method(globalThis, 'fetch', async () => {
    throw new Error('fixture unavailable')
  })
  const response = await request('applications')
  assert.equal(response.status, 503)
  assert.equal((await response.json()).error.code, 'api_unavailable')
})

test('CLI source deploys forward explicit workspace and bearer, never browser cookies', async (t) => {
  t.mock.method(globalThis, 'fetch', async (url: unknown, init?: RequestInit) => {
    assert.equal(new URL(String(url)).pathname, '/api/v1/builds/detect')
    const headers = new Headers(init?.headers)
    assert.equal(headers.get('Authorization'), 'Bearer hs_cli_fixture')
    assert.equal(headers.get('X-Hakopod-Workspace'), 'a'.repeat(32))
    assert.equal(headers.get('Cookie'), null)
    assert.equal(init?.redirect, 'error')
    return Response.json({ mode: 'framework' })
  })
  const response = await forwardAutomationAPI(
    new Request('https://dashboard.example/api/v1/builds/detect', {
      method: 'POST',
      headers: {
        Authorization: 'Bearer hs_cli_fixture',
        'X-Hakopod-Workspace': 'a'.repeat(32),
        Cookie: 'hakopod_session=unrelated',
      },
      body: '{}',
    }),
  )
  assert.equal(response.status, 200)
})

test('public device exchange strips ambient credentials and only accepts POST', async (t) => {
  const mocked = t.mock.method(globalThis, 'fetch', async (_url: unknown, init?: RequestInit) => {
    const headers = new Headers(init?.headers)
    assert.equal(headers.get('Authorization'), null)
    assert.equal(headers.get('Cookie'), null)
    assert.equal(headers.get('X-Hakopod-Workspace'), null)
    return Response.json({ user_code: 'TEST-CODE' })
  })
  assert.equal(
    (
      await forwardAutomationAPI(
        new Request('https://dashboard.example/api/v1/auth/device/start', {
          method: 'POST',
          headers: { Authorization: 'Bearer hs_unrelated', Cookie: 'hakopod_session=unrelated' },
          body: '{}',
        }),
      )
    ).status,
    200,
  )
  assert.equal(
    (await forwardAutomationAPI(new Request('https://dashboard.example/api/v1/auth/device/start')))
      .status,
    405,
  )
  assert.equal(mocked.mock.callCount(), 1)
})
