import assert from 'node:assert/strict'
import test from 'node:test'
import { proxy } from './api-proxy.ts'

test('public MCP proxy preserves client auth and protocol without browser privileges', async (t) => {
  t.mock.method(globalThis, 'fetch', async (url: unknown, init?: RequestInit) => {
    assert.equal(new URL(String(url)).pathname, '/api/v1/mcp')
    assert.equal(new URL(String(url)).search, '?project=demo&environment=development')
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
    'https://dashboard.example/api/v1/mcp?project=demo&environment=development',
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

test('MCP proxy requires explicit bearer credentials and bounds bodies', async (t) => {
  const fetch = t.mock.method(globalThis, 'fetch', async () => {
    throw new Error('must not forward')
  })
  for (const [headers, body, status] of [
    [{ Cookie: 'hakopod_session=browser' }, '{}', 401],
    [{ Authorization: 'Bearer fixture' }, 'x'.repeat(513 * 1024), 413],
  ] as const) {
    const request = new Request('https://dashboard.example/api/v1/mcp', {
      method: 'POST',
      headers,
      body,
    })
    assert.equal((await proxy({ request, params: { _splat: 'v1/mcp' } })).status, status)
  }
  assert.equal(fetch.mock.callCount(), 0)
})
