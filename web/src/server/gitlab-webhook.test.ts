import test from 'node:test'
import assert from 'node:assert/strict'
import { forwardGitLabWebhook } from './gitlab-webhook.ts'

test('GitLab public webhook preserves bytes and its authentication headers without browser authority', async (t) => {
  const payload = new TextEncoder().encode('{"object_kind":"push","message":"é"}\n')
  const forwarded = [
    'X-Gitlab-Token',
    'X-Gitlab-Event',
    'X-Gitlab-Event-UUID',
    'X-Gitlab-Webhook-UUID',
  ]
  let calls = 0
  t.mock.method(globalThis, 'fetch', async (url: unknown, init: RequestInit = {}) => {
    calls++
    assert.equal(String(url).endsWith('/api/v1/webhooks/gitlab'), true)
    assert.deepEqual(init.body, payload)
    const headers = new Headers(init.headers)
    forwarded.forEach((name) => assert.equal(headers.get(name), `unit-test-${name}`))
    for (const name of ['Authorization', 'Cookie', 'Origin', 'X-Untrusted'])
      assert.equal(headers.has(name), false)
    return Response.json(
      { accepted: true },
      { status: 202, headers: { 'Set-Cookie': 'must-not-forward=1' } },
    )
  })
  const response = await forwardGitLabWebhook(
    new Request('http://127.0.0.1:4173/api/v1/webhooks/gitlab', {
      method: 'POST',
      body: payload,
      headers: {
        ...Object.fromEntries(forwarded.map((name) => [name, `unit-test-${name}`])),
        Authorization: 'Bearer unit-test',
        Cookie: 'unit-test=1',
        Origin: 'https://gitlab.com',
        'X-Untrusted': 'unit-test',
      },
    }),
  )
  assert.equal(response.status, 202)
  assert.equal(response.headers.has('Set-Cookie'), false)
  assert.equal(calls, 1)
})
test('GitLab webhook route is exact and rejects methods and oversized bodies before forwarding', async (t) => {
  let calls = 0
  t.mock.method(globalThis, 'fetch', async () => {
    calls++
    return Response.json({})
  })
  assert.equal(
    (await forwardGitLabWebhook(new Request('http://127.0.0.1/api/v1/webhooks/gitlab'))).status,
    405,
  )
  assert.equal(
    (
      await forwardGitLabWebhook(
        new Request('http://127.0.0.1/api/v1/webhooks/gitlab/other', {
          method: 'POST',
          body: '{}',
        }),
      )
    ).status,
    404,
  )
  assert.equal(
    (
      await forwardGitLabWebhook(
        new Request('http://127.0.0.1/api/v1/webhooks/gitlab', {
          method: 'POST',
          body: new Uint8Array(512 * 1024 + 1),
        }),
      )
    ).status,
    413,
  )
  assert.equal(calls, 0)
})
