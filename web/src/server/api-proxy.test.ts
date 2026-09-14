import test from 'node:test'
import assert from 'node:assert/strict'
import { proxy } from './api-proxy.ts'
import { sealSession, sessionCookie } from './session.ts'

const origin = 'http://127.0.0.1:4173'
const token = 'hs_proxy_regression_not_a_real_session_12345'
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
