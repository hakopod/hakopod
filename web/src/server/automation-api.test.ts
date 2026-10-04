import { forwardAutomationAPI } from './automation-api.ts'
import assert from 'node:assert/strict'
import test from 'node:test'
import { proxy } from './api-proxy.ts'

test('external database automation routes keep method boundaries and exclude credential reads', async (t) => {
  const id = 'e'.repeat(32)
  const mock = t.mock.method(globalThis, 'fetch', async (_url: unknown, init?: RequestInit) => {
    assert.equal(new Headers(init?.headers).get('Cookie'), null)
    assert.equal(new Headers(init?.headers).get('Authorization'), 'Bearer hp_external_fixture')
    return Response.json({ accepted: true })
  })
  for (const [path, method] of [
    ['external-databases', 'POST'],
    [`external-databases/${id}`, 'PUT'],
    [`external-databases/${id}/trust`, 'GET'],
    [`external-databases/${id}/connection-plan`, 'POST'],
    [`external-database-operations/${id}`, 'GET'],
  ]) {
    const response = await forwardAutomationAPI(new Request(`https://dashboard.example/api/v1/${path}`, { method, headers: { Authorization: 'Bearer hp_external_fixture', Cookie: 'ambient-session', 'Idempotency-Key': 'external-fixture-key' }, ...(method !== 'GET' ? { body: '{}' } : {}) }))
    assert.equal(response.status, 200, `${method} ${path}`)
  }
  assert.equal(mock.mock.callCount(), 5)
  assert.equal((await forwardAutomationAPI(new Request(`https://dashboard.example/api/v1/external-databases/${id}/credentials`, { headers: { Authorization: 'Bearer hp_external_fixture' } }))).status, 404)
  assert.equal((await forwardAutomationAPI(new Request(`https://dashboard.example/api/v1/external-databases/${id}/trust`, { method: 'POST', headers: { Authorization: 'Bearer hp_external_fixture' }, body: '{}' }))).status, 405)
  assert.equal(mock.mock.callCount(), 5)
})

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

test('database CA trust follows the authenticated read-only API route', async (t) => {
  const id = 'a'.repeat(32)
  const mocked = t.mock.method(globalThis, 'fetch', async (url: unknown, init?: RequestInit) => {
    assert.equal(new URL(String(url)).pathname, `/api/v1/databases/${id}/trust`)
    assert.equal(new Headers(init?.headers).get('Authorization'), 'Bearer hp_fixture')
    return Response.json({ certificate_pem: 'public CA fixture' })
  })
  assert.equal((await request(`databases/${id}/trust`)).status, 200)
  assert.equal((await request(`databases/${id}/trust`, 'POST', '{}')).status, 405)
  assert.equal(mocked.mock.callCount(), 1)
})

test('platform automation preserves exact routes, bearer scope and review authority', async (t) => {
  const id = 'a'.repeat(32)
  const cases = [
    ['managed-platforms', 'GET'],
    ['managed-platforms/catalog', 'GET'],
    [`managed-platforms/${id}`, 'GET'],
    [`managed-platforms/${id}/trust`, 'GET'],
    [`managed-platforms/${id}/operations`, 'GET'],
    [`managed-platforms/${id}/recovery-operations`, 'GET'],
    ['managed-platforms/reviews', 'POST'],
    ['managed-platforms/operations', 'POST'],
    [`managed-platform-operations/${id}`, 'GET'],
    ['managed-platform-recovery/reviews', 'POST'],
    ['managed-platform-recovery/operations', 'POST'],
    [`managed-platform-recovery-operations/${id}`, 'GET'],
    [`managed-platform-recovery-operations/${id}/cancel`, 'POST'],
  ]
  const body = '{"project":"owned","environment":"production","expected_revision":3}'
  const mocked = t.mock.method(globalThis, 'fetch', async (url: unknown, init?: RequestInit) => {
    const target = new URL(String(url))
    assert.ok(cases.some(([path, method]) => target.pathname === `/api/v1/${path}` && init?.method === method))
    assert.equal(target.search, '?project=owned&environment=production')
    const headers = new Headers(init?.headers)
    assert.equal(headers.get('Authorization'), 'Bearer hp_fixture')
    assert.equal(headers.get('Cookie'), null)
    assert.equal(headers.get('X-Hakopod-Workspace'), id)
    assert.equal(headers.get('Idempotency-Key'), 'platform-review-fixture')
    if (init?.method === 'POST') assert.equal(new TextDecoder().decode(init.body as Uint8Array), body)
    return Response.json({ accepted: true })
  })
  for (const [path, method] of cases) {
    const response = await request(`${path}?project=owned&environment=production`, method, method === 'POST' ? body : undefined, {
      Cookie: 'ambient-session', 'X-Hakopod-Workspace': id, 'Idempotency-Key': 'platform-review-fixture',
    })
    assert.equal(response.status, 200, path)
    assert.equal(response.headers.get('Cache-Control'), 'no-store')
    assert.equal((await request(path, method === 'GET' ? 'POST' : 'GET', method === 'GET' ? '{}' : undefined)).status, 405, path)
  }
  assert.equal((await request(`managed-platforms/${id}/trust/extra`)).status, 404)
  assert.equal((await request(`managed-platforms/${id}/credentials`)).status, 404)
  assert.equal((await request(`managed-platforms/${id}/trust`, 'GET', undefined, { Authorization: '', Cookie: 'ambient-session' })).status, 401)
  assert.equal(mocked.mock.callCount(), cases.length)
})

test('public endpoint automation preserves scoped authority, review bodies and method boundaries', async (t) => {
  const id = 'a'.repeat(32)
  const endpoint = 'b'.repeat(32)
  const operation = 'c'.repeat(32)
  const cases = [
    [`databases/${id}/public-endpoint-capabilities`, 'GET'],
    [`databases/${id}/public-endpoint-plan`, 'POST'],
    [`databases/${id}/public-endpoints`, 'GET'],
    [`databases/${id}/public-endpoints`, 'POST'],
    [`databases/${id}/public-endpoints/${endpoint}`, 'DELETE'],
    [`database-public-endpoint-operations/${operation}`, 'GET'],
  ]
  const mocked = t.mock.method(globalThis, 'fetch', async (url: unknown, init?: RequestInit) => {
    assert.ok(cases.some(([path, method]) => new URL(String(url)).pathname === `/api/v1/${path}` && init?.method === method))
    const headers = new Headers(init?.headers)
    assert.equal(headers.get('Authorization'), 'Bearer hp_fixture')
    assert.equal(headers.get('Cookie'), null)
    assert.equal(headers.get('X-Hakopod-Workspace'), id)
    assert.equal(headers.get('Idempotency-Key'), 'endpoint-fixture-key')
    if (init?.method !== 'GET') assert.equal(new TextDecoder().decode(init?.body as Uint8Array), '{"expected_endpoint_revision":3}')
    return Response.json({ accepted: true })
  })
  for (const [path, method] of cases) {
    const response = await request(path, method, method === 'GET' ? undefined : '{"expected_endpoint_revision":3}', {
      Cookie: 'ambient-session', 'X-Hakopod-Workspace': id, 'Idempotency-Key': 'endpoint-fixture-key',
    })
    assert.equal(response.status, 200, `${method} ${path}`)
  }
  assert.equal(mocked.mock.callCount(), cases.length)
  for (const [path, method] of [
    [`databases/${id}/public-endpoint-capabilities`, 'POST'],
    [`databases/${id}/public-endpoint-plan`, 'GET'],
    [`databases/${id}/public-endpoints`, 'DELETE'],
    [`databases/${id}/public-endpoints/${endpoint}`, 'POST'],
    [`database-public-endpoint-operations/${operation}`, 'DELETE'],
  ]) assert.equal((await request(path, method, method === 'GET' ? undefined : '{}')).status, 405)
  assert.equal((await request(`databases/${id}/public-endpoints/${endpoint}/credentials`)).status, 404)
  assert.equal(mocked.mock.callCount(), cases.length)
})

test('database node discovery preserves explicit scope and is read-only', async (t) => {
  const mocked = t.mock.method(globalThis, 'fetch', async (url: unknown, init?: RequestInit) => {
    const target = new URL(String(url))
    assert.equal(target.pathname, '/api/v1/database-placement/nodes')
    assert.equal(target.search, '?project=orders&environment=staging')
    assert.equal(init?.method, 'GET')
    assert.equal(new Headers(init?.headers).get('Authorization'), 'Bearer hp_fixture')
    return Response.json({ items: [], limit: 48 })
  })
  const path = 'database-placement/nodes?project=orders&environment=staging'
  const response = await request(path)
  assert.equal(response.status, 200)
  assert.equal(response.headers.get('Cache-Control'), 'no-store')
  for (const method of ['POST', 'PUT', 'DELETE']) assert.equal((await request(path, method, '{}')).status, 405)
  assert.equal(mocked.mock.callCount(), 1)
})

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

test('an invalid explicit workspace fails before forwarding instead of changing scope', async (t) => {
  t.mock.method(globalThis, 'fetch', async () => {
    throw new Error('must not forward')
  })
  const response = await request('me', 'GET', undefined, {
    'X-Hakopod-Workspace': 'another-workspace',
  })
  assert.equal(response.status, 400)
  assert.equal((await response.json()).error.code, 'invalid_scope')
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
  assert.equal((await request('applications/' + app, 'PATCH')).status, 405)
  assert.equal((await request('keys')).status, 404)
  assert.equal((await request('cloud/workspaces')).status, 404)
  assert.equal((await request('tls/issuers', 'POST', '{}')).status, 405)
  assert.equal((await request('applications/' + app + '/tls/issuers', 'DELETE')).status, 405)
  assert.equal((await request('applications/' + app + '/tls/issuers/foreign')).status, 404)
  assert.equal((await request('deployments', 'POST', 'x'.repeat(1024 * 1024 + 1))).status, 413)
  assert.equal(fetch.mock.callCount(), 0)
})

test('SDK lifecycle routes forward scoped machine credentials and DELETE confirmation bodies', async (t) => {
  const cases = [
    ['POST', 'projects'],
    ['POST', 'projects/demo/environments'],
    ['DELETE', 'projects/demo'],
    ['GET', 'cloud/capabilities'],
    ['DELETE', 'applications/' + app],
    ['POST', 'applications/' + app + '/services/web/scale'],
    ['POST', 'applications/' + app + '/services/web/restart'],
    ['POST', 'applications/' + app + '/services/web/stop'],
    ['POST', 'applications/' + app + '/services/web/resume'],
    ['GET', 'applications/' + app + '/services/web/runtime'],
    ['GET', 'tls/issuers'],
    ['GET', 'applications/' + app + '/tls/issuers'],
    ['POST', 'applications/' + app + '/tls/issuers'],
    ['GET', 'applications/' + app + '/services/web/tls'],
    ['POST', 'applications/' + app + '/services/web/tls'],
    ['GET', 'applications/' + app + '/logs'],
    ['POST', 'applications/' + app + '/rollback'],
    ['POST', 'databases'],
    ['GET', 'databases/db'],
    ['GET', 'databases/db/connections'],
    ['DELETE', 'databases/db'],
    ['GET', 'database-operations/operation'],
    ['POST', 'databases/db/resize-plan'],
    ['POST', 'databases/db/resize'],
    ['POST', 'databases/db/credentials'],
    ['POST', 'databases/db/connection-plan'],
    ['POST', 'databases/db/connect'],
    ['POST', 'virtual-networks/plan'],
    ['POST', 'virtual-networks'],
    ['PUT', 'virtual-networks/private'],
    ['DELETE', 'virtual-networks/private'],
    ['POST', 'secrets/example'],
    ['PUT', 'secrets/example'],
    ['DELETE', 'secrets/example'],
    ['POST', 'deployments/release/cancel'],
  ]
  const payload = JSON.stringify({ confirm_name: 'fixture', expected_revision: 1 })
  for (const [method, path] of cases) {
    const mock = t.mock.method(globalThis, 'fetch', async (url: unknown, init?: RequestInit) => {
      assert.equal(new URL(String(url)).pathname, '/api/v1/' + path)
      assert.equal(new URL(String(url)).searchParams.get('project'), 'fixture')
      const headers = new Headers(init?.headers)
      assert.equal(headers.get('Authorization'), 'Bearer hp_fixture')
      assert.equal(headers.get('X-Hakopod-Workspace'), 'a'.repeat(32))
      assert.equal(headers.get('Cookie'), null)
      assert.equal(headers.get('Idempotency-Key'), 'sdk-fixture-key')
      assert.equal(init?.method, method)
      if (method !== 'GET')
        assert.equal(new TextDecoder().decode(init?.body as Uint8Array), payload)
      return Response.json({ fixture: true })
    })
    const response = await request(
      path + '?project=fixture&environment=development',
      method,
      method === 'GET' ? undefined : payload,
      {
        'X-Hakopod-Workspace': 'a'.repeat(32),
        'Idempotency-Key': 'sdk-fixture-key',
        Cookie: 'unrelated=browser',
      },
    )
    assert.equal(response.status, 200, method + ' ' + path)
    assert.equal(mock.mock.callCount(), 1)
    mock.mock.restore()
  }
})

test('SDK forwarding retains administrative exclusions and bounds DELETE input', async (t) => {
  const upstream = t.mock.method(globalThis, 'fetch', async () => {
    throw new Error('must not forward')
  })
  for (const path of [
    'keys',
    'installation',
    'nodes',
    'auth/security',
    'projects/demo/members',
    'virtual-networks/private/candidates',
  ])
    assert.equal((await request(path)).status, 404, path)
  assert.equal((await request('databases/db', 'DELETE', 'x'.repeat(1024 * 1024 + 1))).status, 413)
  assert.equal(upstream.mock.callCount(), 0)
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
