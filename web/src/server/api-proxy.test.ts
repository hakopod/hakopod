import test from 'node:test'
import assert from 'node:assert/strict'
import { proxy, proxyTimeoutMilliseconds } from './api-proxy.ts'
import { callbackSessionCookie, sealSession, sessionCookie } from './session.ts'

const origin = 'http://127.0.0.1:4173'
const token = 'hs_proxy_regression_not_a_real_session_12345'

test('only database restore planning receives the 90-second proxy budget', () => {
  const database = 'a'.repeat(32)
  assert.equal(proxyTimeoutMilliseconds(`databases/${database}/restore-plan`, 'POST'), 90000)
  assert.equal(proxyTimeoutMilliseconds(`databases/${database}/restore-plan`, 'GET'), 30000)
  assert.equal(proxyTimeoutMilliseconds('databases/not-an-id/restore-plan', 'POST'), 30000)
  assert.equal(proxyTimeoutMilliseconds(`backup-artifacts/${database}/restore-plan`, 'POST'), 30000)
  assert.equal(proxyTimeoutMilliseconds(`databases/${database}/connections`, 'GET'), 30000)
  assert.equal(
    proxyTimeoutMilliseconds(`backup-imports/${database}/archive`, 'PUT'),
    15 * 60 * 1000,
  )
  assert.equal(
    proxyTimeoutMilliseconds('applications/app/services/api/terminal/session/output', 'GET'),
    11 * 60 * 1000,
  )
  assert.equal(proxyTimeoutMilliseconds('deployments/deployment-a/events', 'GET'), 11 * 60 * 1000)
  assert.equal(proxyTimeoutMilliseconds('applications/app/logs', 'GET'), 5 * 60 * 1000)
})

test('database restore planning preserves browser request cancellation', async (t) => {
  const path = `databases/${'a'.repeat(32)}/restore-plan`
  const controller = new AbortController()
  const input = new Request(request(path, 'POST', { artifact_id: 'archive-fixture' }), {
    signal: controller.signal,
  })
  t.mock.method(globalThis, 'fetch', async (_url: unknown, init: RequestInit = {}) => {
    assert.equal(init.signal?.aborted, false)
    controller.abort()
    assert.equal(init.signal?.aborted, true)
    return Response.json({ id: 'plan-fixture' })
  })
  assert.equal((await proxy({ request: input, params: { _splat: path } })).status, 200)
})

test('managed platform catalog forwards scope through the protected session proxy', async (t) => {
  const mocked = t.mock.method(
    globalThis,
    'fetch',
    async (url: unknown, init: RequestInit = {}) => {
      const target = new URL(String(url))
      assert.equal(target.pathname, '/api/v1/managed-platforms/catalog')
      assert.equal(target.search, '?project=owned&environment=production')
      assert.equal(new Headers(init.headers).get('Authorization'), `Bearer ${token}`)
      return Response.json({ items: [] })
    },
  )
  const response = await proxy({
    request: request('managed-platforms/catalog?project=owned&environment=production'),
    params: { _splat: 'managed-platforms/catalog' },
  })
  assert.equal(response.status, 200)
  assert.equal(
    (
      await proxy({
        request: request('managed-platforms/catalog', 'GET', undefined, false),
        params: { _splat: 'managed-platforms/catalog' },
      })
    ).status,
    401,
  )
  mocked.mock.restore()
})
function request(
  path: string,
  method = 'GET',
  body?: object,
  authenticated = true,
  source = origin,
) {
  const headers = new Headers({ Origin: source, 'Content-Type': 'application/json' })
  if (authenticated)
    headers.set('Cookie', sessionCookie(new Request(origin), sealSession(token)).split(';')[0])
  return new Request(`${origin}/api/${path}`, {
    method,
    headers,
    body: body ? JSON.stringify(body) : undefined,
  })
}

test('native runner cancellation and hold recovery forward exact scoped requests with session and origin protection', async (t) => {
  const entries = [
    {
      path: 'applications/app-a/actions/runner-a/jobs/slot-a/cancel',
      method: 'POST',
      body: undefined,
      status: 202,
    },
    {
      path: 'applications/app-a/actions/runner-a/hold',
      method: 'GET',
      body: undefined,
      status: 200,
    },
    {
      path: 'applications/app-a/actions/runner-a/hold/release',
      method: 'POST',
      body: { hold_id: '22222222-2222-4222-8222-222222222222', acknowledge: true },
      status: 200,
    },
  ]
  for (const entry of entries) {
    const mocked = t.mock.method(
      globalThis,
      'fetch',
      async (url: unknown, init: RequestInit = {}) => {
        assert.equal(new URL(String(url)).pathname, `/api/v1/${entry.path}`)
        assert.equal(init.method, entry.method)
        assert.equal(
          init.body,
          entry.method === 'GET' ? undefined : entry.body ? JSON.stringify(entry.body) : '',
        )
        const headers = new Headers(init.headers)
        assert.equal(headers.get('Authorization'), `Bearer ${token}`)
        assert.equal(headers.has('Cookie'), false)
        assert.equal(init.redirect, 'error')
        assert.ok(init.signal)
        return Response.json({ accepted: true }, { status: entry.status })
      },
    )
    const input = request(entry.path, entry.method, entry.body)
    input.headers.set('Authorization', 'Bearer synthetic-untrusted-browser-header')
    const response = await proxy({ request: input, params: { _splat: entry.path } })
    assert.equal(response.status, entry.status)
    assert.equal(response.headers.get('Cache-Control'), 'no-store')
    assert.deepEqual(await response.json(), { accepted: true })
    assert.equal(
      (
        await proxy({
          request: request(entry.path, entry.method, entry.body, false),
          params: { _splat: entry.path },
        })
      ).status,
      401,
    )
    if (entry.method === 'POST')
      assert.equal(
        (
          await proxy({
            request: request(
              entry.path,
              entry.method,
              entry.body,
              true,
              'https://untrusted.invalid',
            ),
            params: { _splat: entry.path },
          })
        ).status,
        403,
      )
    const wrongMethod = entry.method === 'POST' ? 'GET' : 'POST'
    const denied = await proxy({
      request: request(entry.path, wrongMethod),
      params: { _splat: entry.path },
    })
    assert.equal(denied.status, 405)
    assert.equal(denied.headers.get('Allow'), entry.method)
    assert.equal(
      (
        await proxy({
          request: request(entry.path + '/extra'),
          params: { _splat: entry.path + '/extra' },
        })
      ).status,
      404,
    )
    assert.equal(mocked.mock.callCount(), 1)
    mocked.mock.restore()
  }
})

test('preview and framework requests preserve scoped paths and mutation protections', async (t) => {
  const cases: { path: string; method: string; body?: object; query?: string }[] = [
    {
      path: 'deployment-secret-requirements',
      method: 'POST',
      body: { project: 'demo', environment: 'preview', spec: { name: 'app', services: {} } },
    },
    {
      path: 'secrets/requirements',
      method: 'POST',
      query: '?project=demo&environment=preview&application=app',
      body: { value: 'synthetic-secret' },
    },
    { path: 'applications/app-a/volume-resizes', method: 'GET' },
    {
      path: 'applications/app-a/volume-resizes/plan',
      method: 'POST',
      body: { claim: 'db-data', size_gib: 1, expected_revision: 2 },
    },
    {
      path: 'applications/app-a/volume-resizes',
      method: 'POST',
      body: {
        claim: 'db-data',
        size_gib: 1,
        expected_revision: 2,
        source_uid: 'reviewed-pvc',
        confirm_downtime: true,
      },
    },
    ...['retry', 'cancel', 'retain', 'delete-original'].map((action) => ({
      path: `applications/app-a/volume-resizes/resize-a/${action}`,
      method: 'POST',
      body: { confirm_claim: 'db-data' },
    })),
    { path: 'requests', method: 'GET', query: '?project=demo&service=api&status=5' },
    { path: 'applications/app-a/services/api/requests/routing', method: 'GET' },
    { path: 'applications/app-a/tls/issuers', method: 'GET' },
    {
      path: 'applications/app-a/tls/issuers',
      method: 'POST',
      body: { name: 'custom', email: 'app@example.test', production: false },
    },
    {
      path: 'applications/app-a/services/api/tls',
      method: 'POST',
      body: { issuer: 'custom', issuer_kind: 'Issuer', expected_revision: 1 },
    },
    {
      path: 'applications/app-a/services/api/move-plan',
      method: 'POST',
      body: {
        destination_id: 'app-b',
        destination_service: 'worker',
        source_revision: 1,
        destination_revision: 2,
      },
    },
    {
      path: 'applications/app-a/services/api/move',
      method: 'POST',
      body: {
        destination_id: 'app-b',
        destination_service: 'worker',
        source_revision: 1,
        destination_revision: 2,
      },
    },
    { path: 'applications/app-a/service-moves', method: 'GET' },
    { path: 'applications/app-a/service-moves/move-a/finish', method: 'POST', body: {} },
    { path: 'git/connections/connection-a/repositories', method: 'GET', query: '?page=2' },
    {
      path: 'compose/convert',
      method: 'POST',
      body: { compose: 'services:\n  web:\n    image: nginx:alpine', name: 'web' },
    },
    { path: 'applications/app-a/previews', method: 'GET', query: '?cursor=next' },
    { path: 'previews/preview-a', method: 'GET' },
    {
      path: 'applications/app-a/previews',
      method: 'POST',
      body: {
        name: 'pr-12',
        expected_parent_revision: 3,
        discard_on_expiry: true,
        toml: 'schema = 1',
      },
    },
    { path: 'previews/preview-a', method: 'DELETE', body: { confirmation: 'pr-12' } },
    {
      path: 'builds/detect',
      method: 'POST',
      body: { project: 'demo', environment: 'preview', repository: 'team/app', branch: 'main' },
    },
  ]
  for (const entry of cases) {
    let calls = 0
    const mock = t.mock.method(
      globalThis,
      'fetch',
      async (url: unknown, init: RequestInit = {}) => {
        calls++
        assert.equal(new URL(String(url)).pathname, `/api/v1/${entry.path}`)
        assert.equal(new URL(String(url)).search, entry.query || '')
        assert.equal(init.method, entry.method)
        assert.equal(init.body, entry.body ? JSON.stringify(entry.body) : undefined)
        const headers = new Headers(init.headers)
        assert.equal(headers.get('Authorization'), `Bearer ${token}`)
        assert.equal(headers.has('Cookie'), false)
        if (entry.method !== 'GET') assert.equal(headers.get('Idempotency-Key'), 'reviewed-preview')
        return Response.json({ accepted: true })
      },
    )
    const input = request(entry.path + (entry.query || ''), entry.method, entry.body)
    input.headers.set('Idempotency-Key', 'reviewed-preview')
    assert.equal((await proxy({ request: input, params: { _splat: entry.path } })).status, 200)
    assert.equal(
      (
        await proxy({
          request: request(entry.path, entry.method, entry.body, false),
          params: { _splat: entry.path },
        })
      ).status,
      401,
    )
    if (entry.method !== 'GET') {
      assert.equal(
        (
          await proxy({
            request: request(
              entry.path,
              entry.method,
              entry.body,
              true,
              'https://untrusted.invalid',
            ),
            params: { _splat: entry.path },
          })
        ).status,
        403,
      )
    }
    assert.equal(
      (
        await proxy({
          request: request(entry.path + '/extra'),
          params: { _splat: entry.path + '/extra' },
        })
      ).status,
      404,
    )
    assert.equal(calls, 1)
    mock.mock.restore()
  }
})

test('secret provider proxy keeps configuration behind session and origin checks', async (t) => {
  let calls = 0
  t.mock.method(globalThis, 'fetch', async (_url: unknown, init: RequestInit = {}) => {
    calls++
    assert.equal(new Headers(init.headers).get('Authorization'), `Bearer ${token}`)
    return Response.json({ name: 'company', kind: 'vault' })
  })
  for (const path of ['secret-providers', 'secret-providers/company']) {
    assert.equal((await proxy({ request: request(path), params: { _splat: path } })).status, 200)
    assert.equal(
      (await proxy({ request: request(path, 'GET', undefined, false), params: { _splat: path } }))
        .status,
      401,
    )
  }
  const path = 'secret-providers/company'
  assert.equal(
    (
      await proxy({
        request: request(path, 'PUT', {}, true, 'https://untrusted.invalid'),
        params: { _splat: path },
      })
    ).status,
    403,
  )
  assert.equal(
    (
      await proxy({
        request: request(path + '/credentials'),
        params: { _splat: path + '/credentials' },
      })
    ).status,
    404,
  )
  assert.equal(calls, 2)
})

test('the browser proxy forwards alarm reads and mutations with sealed session authority', async (t) => {
  const cases = [
    { path: 'alarms', query: '?project=demo&status=active&limit=25', method: 'GET' },
    { path: 'alarm-settings', query: '?project=demo&environment=staging', method: 'GET' },
    {
      path: 'alarm-settings',
      query: '?project=demo',
      method: 'PUT',
      body: { enabled: true, hold_seconds: 120, email_enabled: false, expected_revision: 1 },
    },
    { path: 'alarms/incident-a/read', query: '', method: 'POST', body: { expected_event_id: 42 } },
    {
      path: 'alarms/incident-a/acknowledge',
      query: '',
      method: 'POST',
      body: { expected_event_id: 42 },
    },
  ]
  for (const item of cases) {
    let calls = 0
    const mock = t.mock.method(
      globalThis,
      'fetch',
      async (url: unknown, init: RequestInit = {}) => {
        calls++
        assert.equal(new URL(String(url)).pathname, `/api/v1/${item.path}`)
        assert.equal(new URL(String(url)).search, item.query)
        assert.equal(init.method, item.method)
        const headers = new Headers(init.headers)
        assert.equal(headers.get('Authorization'), `Bearer ${token}`)
        assert.equal(headers.has('Cookie'), false)
        assert.equal(headers.has('Origin'), false)
        assert.equal(init.body, item.body ? JSON.stringify(item.body) : undefined)
        return Response.json({ test: true }, { headers: { 'Set-Cookie': 'upstream=private' } })
      },
    )
    const response = await proxy({
      request: request(item.path + item.query, item.method, item.body),
      params: { _splat: item.path },
    })
    assert.equal(response.status, 200)
    assert.deepEqual(await response.json(), { test: true })
    assert.equal(response.headers.has('Set-Cookie'), false)
    assert.match(response.headers.get('Cache-Control') || '', /no-store/)
    assert.equal(calls, 1)
    mock.mock.restore()
  }
})

test('alarm proxy paths retain authentication, CSRF and exact endpoint boundaries', async (t) => {
  let calls = 0
  t.mock.method(globalThis, 'fetch', async () => {
    calls++
    return Response.json({})
  })
  for (const path of ['alarms', 'alarm-settings']) {
    const response = await proxy({
      request: request(path, 'GET', undefined, false),
      params: { _splat: path },
    })
    assert.equal(response.status, 401)
  }
  for (const path of [
    'alarm-settings',
    'alarms/incident-a/read',
    'alarms/incident-a/acknowledge',
  ]) {
    const response = await proxy({
      request: request(path, 'POST', {}, true, 'https://untrusted.invalid'),
      params: { _splat: path },
    })
    assert.equal(response.status, 403)
  }
  for (const path of [
    'alarms/incident-a/delete',
    'alarms/incident-a/read/more',
    'alarm-settings/more',
  ]) {
    const response = await proxy({ request: request(path), params: { _splat: path } })
    assert.equal(response.status, 404)
  }
  assert.equal(calls, 0)
})

test('delivery and certificate routes preserve session and CSRF boundaries', async (t) => {
  let calls = 0
  t.mock.method(globalThis, 'fetch', async (_url: unknown, init: RequestInit = {}) => {
    calls++
    assert.equal(new Headers(init.headers).get('Authorization'), `Bearer ${token}`)
    return Response.json({ items: [] })
  })
  for (const suffix of ['delivery', 'certificates']) {
    const path = `applications/app-a/services/smtp/${suffix}`
    const response = await proxy({ request: request(path), params: { _splat: path } })
    assert.equal(response.status, 200)
    assert.equal(
      (await proxy({ request: request(path, 'GET', undefined, false), params: { _splat: path } }))
        .status,
      401,
    )
  }
  const path = 'applications/app-a/services/smtp/certificates'
  assert.equal(
    (
      await proxy({
        request: request(path, 'POST', { hostname: 'mail.example.test', from_ingress: true }),
        params: { _splat: path },
      })
    ).status,
    200,
  )
  assert.equal(
    (
      await proxy({
        request: request(path, 'POST', {}, true, 'https://unrelated.test'),
        params: { _splat: path },
      })
    ).status,
    403,
  )
  assert.equal(
    (await proxy({ request: request(path + '/raw-key'), params: { _splat: path + '/raw-key' } }))
      .status,
    404,
  )
  assert.equal(calls, 3)
})

test('audit export preserves bounded pagination and sealed authorization', async (t) => {
  let calls = 0
  t.mock.method(globalThis, 'fetch', async (url: unknown, init: RequestInit = {}) => {
    calls++
    assert.equal(new URL(String(url)).pathname, '/api/v1/audit/export')
    assert.equal(new Headers(init.headers).get('Authorization'), `Bearer ${token}`)
    return new Response('id,action\n1,fixture\n', {
      headers: { 'Content-Type': 'text/csv', 'X-Hakopod-Next-Cursor': '42' },
    })
  })
  const signedOut = await proxy({
    request: request('audit/export', 'GET', undefined, false),
    params: { _splat: 'audit/export' },
  })
  assert.equal(signedOut.status, 401)
  assert.equal(calls, 0)
  const result = await proxy({
    request: request('audit/export?identity_id=' + 'a'.repeat(32)),
    params: { _splat: 'audit/export' },
  })
  assert.equal(result.headers.get('Content-Type'), 'text/csv')
  assert.equal(result.headers.get('X-Hakopod-Next-Cursor'), '42')
  assert.equal(await result.text(), 'id,action\n1,fixture\n')
  assert.equal(calls, 1)
})

test('installation settings require sealed sessions and same-origin writes', async (t) => {
  let calls = 0
  t.mock.method(globalThis, 'fetch', async (_url: unknown, init: RequestInit = {}) => {
    calls++
    assert.equal(new Headers(init.headers).get('Authorization'), `Bearer ${token}`)
    return Response.json({ revision: 1 })
  })
  for (const path of [
    'installation/smtp',
    'installation/smtp/test',
    ...['github', 'google', 'gitlab', 'oidc'].map(
      (provider) => `installation/login-providers/${provider}`,
    ),
  ]) {
    const method = path.endsWith('/test') ? 'POST' : 'GET'
    assert.equal(
      (
        await proxy({
          request: request(path, method, method === 'POST' ? {} : undefined, false),
          params: { _splat: path },
        })
      ).status,
      401,
    )
    assert.equal(
      (
        await proxy({
          request: request(path, method, method === 'POST' ? {} : undefined),
          params: { _splat: path },
        })
      ).status,
      200,
    )
    assert.equal(
      (
        await proxy({
          request: request(path, 'PUT', {}, true, 'https://untrusted.invalid'),
          params: { _splat: path },
        })
      ).status,
      403,
    )
  }
  assert.equal(calls, 6)
  for (const path of [
    'installation/smtp/password',
    'installation/login-providers/other',
    'installation/login-providers/oidc/secret',
  ])
    assert.equal((await proxy({ request: request(path), params: { _splat: path } })).status, 404)
})

test('OIDC start is public while forwarding only its temporary OAuth state', async (t) => {
  t.mock.method(globalThis, 'fetch', async (url: unknown, init: RequestInit = {}) => {
    assert.match(String(url), /auth\/oauth\/oidc\/start$/)
    assert.equal(new Headers(init.headers).has('Authorization'), false)
    assert.equal(new Headers(init.headers).has('Cookie'), false)
    return new Response(null, {
      status: 302,
      headers: {
        Location: 'https://identity.example.test/authorize',
        'Set-Cookie': 'hakopod_oauth=oidc-fixture-state; Path=/; HttpOnly; SameSite=Lax',
      },
    })
  })
  const path = 'v1/auth/oauth/oidc/start'
  const response = await proxy({
    request: request(path, 'GET', undefined, false),
    params: { _splat: path },
  })
  assert.equal(response.status, 302)
  assert.equal(response.headers.get('Location'), 'https://identity.example.test/authorize')
  assert.match(response.headers.get('Set-Cookie') || '', /HttpOnly.*SameSite=Lax/)
  assert.equal(response.headers.get('Set-Cookie')?.includes('Domain='), false)
})

test('Slack callback uses a short-lived callback session and forwards only an approved relative redirect', async (t) => {
  const callback = new Request(
    `${origin}/api/integrations/slack/callback?code=fixture&state=fixture`,
  )
  callback.headers.set(
    'Cookie',
    callbackSessionCookie(callback, 'hakopod_slack_callback', token).split(';')[0],
  )
  const mocked = t.mock.method(
    globalThis,
    'fetch',
    async (url: unknown, init: RequestInit = {}) => {
      assert.equal(new URL(String(url)).pathname, '/api/v1/integrations/slack/callback')
      assert.equal(new Headers(init.headers).get('Authorization'), `Bearer ${token}`)
      assert.equal(init.redirect, 'manual')
      return new Response(null, {
        status: 303,
        headers: { Location: '/settings/integrations/slack?connected=1' },
      })
    },
  )
  const response = await proxy({
    request: callback,
    params: { _splat: 'v1/integrations/slack/callback' },
  })
  assert.equal(response.status, 303)
  assert.equal(response.headers.get('Location'), '/settings/integrations/slack?connected=1')
  assert.match(response.headers.get('Set-Cookie') || '', /hakopod_slack_callback=.*Max-Age=0/)
  mocked.mock.restore()
})

test('named Git CRUD and source OAuth completion require the initiating browser authority', async (t) => {
  let calls = 0
  t.mock.method(globalThis, 'fetch', async (_url: unknown, init: RequestInit = {}) => {
    calls++
    assert.equal(new Headers(init.headers).get('Authorization'), `Bearer ${token}`)
    assert.equal(new Headers(init.headers).has('Cookie'), false)
    return Response.json({ id: 'fixture' })
  })
  for (const path of [
    'git/connections',
    'git/connections/fixture',
    'git/connections/fixture/authorize',
    'git/connections/fixture/oauth/complete',
    'git/oauth/complete',
    'git/github/start',
    'git/github/complete',
    'git/github/install/complete',
    'git/connections/fixture/github/setup',
    'installation/logs/query',
  ]) {
    assert.equal(
      (await proxy({ request: request(path, 'POST', {}, false), params: { _splat: path } })).status,
      401,
    )
    assert.equal(
      (
        await proxy({
          request: request(path, 'POST', {}, true, 'https://untrusted.invalid'),
          params: { _splat: path },
        })
      ).status,
      403,
    )
    assert.equal(
      (await proxy({ request: request(path, 'POST', {}), params: { _splat: path } })).status,
      200,
    )
  }
  for (const path of [
    'git/connections/fixture/credentials',
    'git/oauth/complete/extra',
    'git/connections/fixture/authorize/extra',
  ])
    assert.equal(
      (await proxy({ request: request(path, 'POST', {}), params: { _splat: path } })).status,
      404,
    )
  assert.equal(calls, 10)
})

test('resource deletion proxy requires a session and same-origin request', async (t) => {
  let calls = 0
  t.mock.method(globalThis, 'fetch', async (_url: unknown, init: RequestInit = {}) => {
    calls++
    assert.equal(init.method, 'DELETE')
    assert.equal(new Headers(init.headers).get('Authorization'), `Bearer ${token}`)
    return Response.json({ status: 'deleted' })
  })
  for (const path of ['projects/empty-project', 'applications/empty-application']) {
    const body = { confirm_name: 'reviewed-name', expected_revision: 2 }
    assert.equal(
      (await proxy({ request: request(path, 'DELETE', body), params: { _splat: path } })).status,
      200,
    )
    assert.equal(
      (await proxy({ request: request(path, 'DELETE', body, false), params: { _splat: path } }))
        .status,
      401,
    )
    assert.equal(
      (
        await proxy({
          request: request(path, 'DELETE', body, true, 'https://untrusted.invalid'),
          params: { _splat: path },
        })
      ).status,
      403,
    )
  }
  assert.equal(calls, 2)
})

test('stop and resume forward reviewed mutations without bypassing session or origin checks', async (t) => {
  for (const action of ['stop', 'resume']) {
    const path = `applications/app-a/services/api/${action}`
    const body = { expected_revision: 7 }
    let calls = 0
    const mock = t.mock.method(
      globalThis,
      'fetch',
      async (url: unknown, init: RequestInit = {}) => {
        calls++
        assert.equal(new URL(String(url)).pathname, `/api/v1/${path}`)
        assert.equal(init.method, 'POST')
        assert.equal(init.body, JSON.stringify(body))
        const headers = new Headers(init.headers)
        assert.equal(headers.get('Authorization'), `Bearer ${token}`)
        assert.equal(headers.get('Idempotency-Key'), 'reviewed-action')
        assert.equal(headers.has('Cookie'), false)
        return Response.json({ revision: 8 }, { status: 202 })
      },
    )
    const mutation = request(path, 'POST', body)
    mutation.headers.set('Idempotency-Key', 'reviewed-action')
    const result = await proxy({ request: mutation, params: { _splat: path } })
    assert.equal(result.status, 202)
    assert.deepEqual(await result.json(), { revision: 8 })
    assert.match(result.headers.get('Cache-Control') || '', /no-store/)
    assert.equal(
      (
        await proxy({
          request: request(path, 'POST', body, false),
          params: { _splat: path },
        })
      ).status,
      401,
    )
    assert.equal(
      (
        await proxy({
          request: request(path, 'POST', body, true, 'https://untrusted.invalid'),
          params: { _splat: path },
        })
      ).status,
      403,
    )
    assert.equal(
      (
        await proxy({
          request: request(path + '/extra', 'POST', body),
          params: { _splat: path + '/extra' },
        })
      ).status,
      404,
    )
    assert.equal(calls, 1)
    mock.mock.restore()
  }
})

test('database archive proxy streams raw bytes with session, origin and size bounds', async (t) => {
  const path = `backup-imports/${'a'.repeat(32)}/archive`
  const payload = new Uint8Array([0, 1, 2, 255, 10])
  let calls = 0
  t.mock.method(globalThis, 'fetch', async (_url: unknown, init: RequestInit = {}) => {
    calls++
    assert.equal(new Headers(init.headers).get('Authorization'), `Bearer ${token}`)
    assert.equal(new Headers(init.headers).get('Content-Type'), 'application/octet-stream')
    assert.ok(init.body instanceof ReadableStream)
    assert.deepEqual(new Uint8Array(await new Response(init.body).arrayBuffer()), payload)
    return Response.json({ id: 'artifact-fixture' }, { status: 201 })
  })
  const make = (source = origin, size = String(payload.byteLength)) => {
    const r = request(path, 'PUT', undefined, true, source)
    const headers = new Headers(r.headers)
    headers.set('Content-Type', 'application/octet-stream')
    headers.set('Content-Length', size)
    return new Request(r.url, { method: 'PUT', headers, body: payload })
  }
  assert.equal((await proxy({ request: make(), params: { _splat: path } })).status, 201)
  assert.equal(
    (await proxy({ request: make('https://untrusted.invalid'), params: { _splat: path } })).status,
    403,
  )
  assert.equal(
    (await proxy({ request: make(origin, String(64 * 1024 * 1024 + 1)), params: { _splat: path } }))
      .status,
    413,
  )
  assert.equal(calls, 1)
})
