import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import test from 'node:test'
import { allowed, proxy } from './api-proxy.ts'
import { sealSession, sessionCookie } from './session.ts'

// These endpoints have their own session-sealing/signature-aware transports.
const alternate = new Set([
  '/mcp',
  '/auth/setup',
  '/auth/login',
  '/auth/logout',
  '/auth/mfa/complete',
  '/auth/invites/accept',
  '/auth/register',
  '/auth/register/verify',
  '/auth/password/forgot',
  '/auth/password/reset',
  '/auth/oauth/{provider}/start',
  '/auth/oauth/{provider}/callback',
  '/webhooks/git/{connection}',
  '/webhooks/github-app/{app}',
  '/webhooks/github',
  '/webhooks/gitlab',
  // CLI device exchanges call the API directly, without browser session cookies.
  '/auth/device/start',
  '/auth/device/token',
])
test('every documented API path has dashboard coverage or an explicit alternate transport', () => {
  const spec = JSON.parse(
    readFileSync(new URL('../../../api/openapi.json', import.meta.url), 'utf8'),
  )
  for (const path of Object.keys(spec.paths)) {
    const concrete = path
      .slice(1)
      .replace('{provider}', 'github')
      .replace('{action}', 'retry')
      .replace(/\{[^}]+\}/g, 'a'.repeat(32))
    assert.ok(alternate.has(path) || allowed.some((pattern) => pattern.test(concrete)), path)
  }
  for (const path of alternate) assert.ok(spec.paths[path], `obsolete exception ${path}`)
})

test('capabilities, schema and idempotency lookups use the authenticated proxy', async (t) => {
  for (const path of [
    'cloud/capabilities',
    'openapi.json',
    'idempotency/deploy-12345678',
    'placement/nodes',
    'database-placement/nodes',
    `databases/${'a'.repeat(32)}/public-endpoint-capabilities`,
    'managed-platforms',
    `managed-platforms/${'a'.repeat(32)}/operations`,
    `managed-platform-operations/${'a'.repeat(32)}`,
    `managed-platforms/${'a'.repeat(32)}/recovery-operations`,
    `managed-platform-recovery-operations/${'a'.repeat(32)}`,
  ]) {
    const input = new Request(`http://127.0.0.1/api/${path}`)
    input.headers.set('Cookie', sessionCookie(input, sealSession('fixture-token')).split(';')[0])
    const mocked = t.mock.method(globalThis, 'fetch', async (url: unknown, init?: RequestInit) => {
      assert.equal(new URL(String(url)).pathname, `/api/v1/${path}`)
      assert.equal(new Headers(init?.headers).get('Authorization'), 'Bearer fixture-token')
      return Response.json({ accepted: true })
    })
    const response = await proxy({ request: input, params: { _splat: path } })
    assert.equal(response.status, 200)
    assert.deepEqual(await response.json(), { accepted: true })
    assert.equal(mocked.mock.callCount(), 1)
    mocked.mock.restore()
  }
})

test('platform recovery writes preserve authentication, origin and retry authority', async (t) => {
  for (const path of [
    'managed-platform-recovery/reviews',
    'managed-platform-recovery/operations',
    `managed-platform-recovery-operations/${'a'.repeat(32)}/cancel`,
  ]) {
    const body = '{\n "project": "fixture", "environment": "development"\n}'
    const input = new Request(`http://127.0.0.1/api/${path}`, {
      method: 'POST',
      headers: { Origin: 'http://127.0.0.1', 'Idempotency-Key': 'recovery-retry-123' },
      body,
    })
    const unauthenticated = await proxy({ request: input.clone(), params: { _splat: path } })
    assert.equal(unauthenticated.status, 401)
    input.headers.set('Cookie', sessionCookie(input, sealSession('fixture-token')).split(';')[0])
    const mocked = t.mock.method(globalThis, 'fetch', async (url: unknown, init?: RequestInit) => {
      assert.equal(new URL(String(url)).pathname, `/api/v1/${path}`)
      assert.equal(init?.method, 'POST')
      assert.equal(new Headers(init?.headers).get('Authorization'), 'Bearer fixture-token')
      assert.equal(new Headers(init?.headers).get('Idempotency-Key'), 'recovery-retry-123')
      assert.equal(init?.body, body)
      return Response.json({ error: { code: 'managed_platform_recovery_unavailable' } }, { status: 503 })
    })
    const response = await proxy({ request: input, params: { _splat: path } })
    assert.equal(response.status, 503)
    assert.equal(mocked.mock.callCount(), 1)
    mocked.mock.restore()
  }
})

test('platform recovery transport rejects unregistered paths', () => {
  for (const path of [
    'managed-platform-recovery/admin',
    `managed-platform-recovery-operations/${'a'.repeat(32)}/execute`,
    `managed-platforms/${'a'.repeat(32)}/recovery-operations/cancel`,
  ]) assert.equal(allowed.some((pattern) => pattern.test(path)), false, path)
})

test('deployment events preserve streaming, reconnection cursor and cancellation', async (t) => {
  const path = 'deployments/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa/events'
  const input = new Request(`http://127.0.0.1/api/${path}`, { headers: { 'Last-Event-ID': '42' } })
  input.headers.set('Cookie', sessionCookie(input, sealSession('fixture-token')).split(';')[0])
  const events = 'id: 43\nevent: deployment\ndata: {"stage":"ready"}\n\n'
  t.mock.method(globalThis, 'fetch', async (_url: unknown, init?: RequestInit) => {
    assert.equal(new Headers(init?.headers).get('Last-Event-ID'), '42')
    assert.ok(init?.signal)
    return new Response(events, { headers: { 'Content-Type': 'text/event-stream' } })
  })
  const response = await proxy({ request: input, params: { _splat: path } })
  assert.equal(response.headers.get('X-Accel-Buffering'), 'no')
  assert.equal(response.headers.get('Content-Type'), 'text/event-stream')
  assert.equal(response.headers.get('Cache-Control'), 'no-store')
  assert.equal(await response.text(), events)
})
