import assert from 'node:assert/strict'
import test from 'node:test'
import { proxy } from './api-proxy.ts'
import { forwardOAuthMetadata } from './mcp.ts'

test('public MCP proxy preserves client auth and protocol without browser privileges', async (t) => {
  t.mock.method(globalThis, 'fetch', async (url: unknown, init?: RequestInit) => {
    assert.equal(new URL(String(url)).pathname, '/api/v1/mcp')
    assert.equal(
      new URL(String(url)).search,
      '?project=demo&environment=development&allow_deploy=false&allow_write=true&allow_exec=true&allow_sql=true&allow_sql_write=false',
    )
    const headers = new Headers(init?.headers)
    assert.equal(headers.get('Authorization'), 'Bearer hp_client_key')
    assert.equal(headers.get('Accept'), 'application/json, text/event-stream')
    assert.equal(headers.get('Mcp-Session-Id'), 'test-session')
    assert.equal(headers.get('MCP-Protocol-Version'), '2025-06-18')
    assert.equal(headers.get('Origin'), 'https://untrusted.example')
    for (const name of ['Cookie', 'X-Hakopod-Workspace', 'X-Forwarded-For'])
      assert.equal(headers.has(name), false)
    assert.equal(init?.redirect, 'error')
    return new Response(null, {
      status: 202,
      headers: { 'Mcp-Session-Id': 'test-session', 'Set-Cookie': 'secret' },
    })
  })
  const request = new Request(
    'https://dashboard.example/api/v1/mcp?project=demo&environment=development&allow_deploy=false&allow_write=true&allow_exec=true&allow_sql=true&allow_sql_write=false',
    {
      method: 'POST',
      headers: {
        Authorization: 'Bearer hp_client_key',
        Accept: 'application/json, text/event-stream',
        'Content-Type': 'application/json',
        'Mcp-Session-Id': 'test-session',
        'MCP-Protocol-Version': '2025-06-18',
        Origin: 'https://untrusted.example',
        Cookie: 'hakopod_session=browser',
        'X-Hakopod-Workspace': 'other',
        'X-Forwarded-For': '127.0.0.1',
      },
      body: '{"jsonrpc":"2.0","method":"notifications/initialized"}',
    },
  )
  const response = await proxy({ request, params: { _splat: 'v1/mcp' } })
  assert.equal(response.status, 202)
  assert.equal(response.headers.get('Mcp-Session-Id'), 'test-session')
  assert.equal(response.headers.has('Set-Cookie'), false)
})

test('MCP proxy bounds bodies', async (t) => {
  const fetch = t.mock.method(globalThis, 'fetch', async () => {
    throw new Error('must not forward')
  })
  const request = new Request('https://dashboard.example/api/v1/mcp', {
    method: 'POST',
    headers: { Authorization: 'Bearer fixture' },
    body: 'x'.repeat(513 * 1024),
  })
  assert.equal((await proxy({ request, params: { _splat: 'v1/mcp' } })).status, 413)
  assert.equal(fetch.mock.callCount(), 0)
})

test('MCP proxy without a bearer returns the API sign-in challenge, never the browser session', async (t) => {
  const challenge =
    'Bearer resource_metadata="https://dashboard.example/.well-known/oauth-protected-resource/api/v1/mcp?project=demo"'
  const fetch = t.mock.method(globalThis, 'fetch', async (url: unknown, init?: RequestInit) => {
    assert.equal(String(url), 'http://127.0.0.1:8080/api/v1/mcp?project=demo')
    const headers = new Headers(init?.headers)
    assert.equal(headers.get('Authorization'), null)
    assert.equal(headers.get('Cookie'), null)
    assert.equal(init?.body, undefined)
    return Response.json({}, { status: 401, headers: { 'WWW-Authenticate': challenge } })
  })
  const request = new Request('https://dashboard.example/api/v1/mcp?project=demo', {
    method: 'POST',
    headers: { Cookie: 'hakopod_session=browser', Authorization: 'Basic x' },
    body: '{}',
  })
  const response = await proxy({ request, params: { _splat: 'v1/mcp' } })
  assert.equal(response.status, 401)
  assert.equal(response.headers.get('WWW-Authenticate'), challenge)
  assert.equal(fetch.mock.callCount(), 1)
})

test('OAuth metadata is answered by the API and other well-known paths are not', async (t) => {
  const fetch = t.mock.method(globalThis, 'fetch', async (url: unknown) => {
    assert.equal(
      String(url),
      'http://127.0.0.1:8080/.well-known/oauth-protected-resource/api/v1/mcp?project=demo',
    )
    return Response.json({ resource: 'x' })
  })
  const ok = await forwardOAuthMetadata(
    new Request(
      'https://dashboard.example/.well-known/oauth-protected-resource/api/v1/mcp?project=demo',
    ),
  )
  assert.equal(ok.status, 200)
  assert.deepEqual(await ok.json(), { resource: 'x' })
  for (const path of ['/.well-known/security.txt', '/.well-known/oauth-protected-resource/x'])
    assert.equal(
      (await forwardOAuthMetadata(new Request('https://dashboard.example' + path))).status,
      404,
    )
  assert.equal(fetch.mock.callCount(), 1)
})
