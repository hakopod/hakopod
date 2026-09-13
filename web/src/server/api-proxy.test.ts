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
