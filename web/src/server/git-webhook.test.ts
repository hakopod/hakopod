import test from 'node:test'
import assert from 'node:assert/strict'
import { forwardNamedGitWebhook } from './git-webhook.ts'

test('named webhooks keep signed bytes and never forward browser credentials', async (t) => {
  let count = 0
  const bytes = new TextEncoder().encode('{"project":"example"}\n')
  t.mock.method(globalThis, 'fetch', async (url: unknown, init: RequestInit = {}) => {
    count++
    assert.match(
      String(url),
      /\/api\/v1\/webhooks\/(git\/fixture-id|github-app\/123|nodes\/[a-f0-9]{32}\/git\/[a-f0-9]{32})$/,
    )
    assert.deepEqual(init.body, bytes)
    const headers = new Headers(init.headers)
    for (const name of ['Authorization', 'Cookie', 'Origin', 'X-Forwarded-For'])
      assert.equal(headers.has(name), false)
    assert.equal(headers.get('X-Gitlab-Token'), 'fixture-signature')
    assert.equal(headers.get('X-Gitlab-Webhook-UUID'), 'fixture-delivery')
    assert.equal(init.redirect, 'error')
    return Response.json(
      { accepted: true },
      { status: 202, headers: { 'Set-Cookie': 'must-not-forward=1' } },
    )
  })
  for (const endpoint of [
    'git/fixture-id',
    'github-app/123',
    `nodes/${'a'.repeat(32)}/git/${'b'.repeat(32)}`,
  ]) {
    const response = await forwardNamedGitWebhook(
      new Request(`http://localhost/api/v1/webhooks/${endpoint}`, {
        method: 'POST',
        body: bytes,
        headers: {
          Authorization: 'Bearer browser',
          Cookie: 'fixture-cookie=1',
          Origin: 'https://provider.test',
          'X-Forwarded-For': '1.2.3.4',
          'X-Gitlab-Token': 'fixture-signature',
          'X-Gitlab-Webhook-UUID': 'fixture-delivery',
        },
      }),
    )
    assert.equal(response.status, 202)
    assert.equal(response.headers.has('Set-Cookie'), false)
  }
  assert.equal(count, 3)
})
test('named webhooks reject malformed paths, methods and oversized bodies', async (t) => {
  let called = false
  t.mock.method(globalThis, 'fetch', async () => {
    called = true
    return Response.json({})
  })
  for (const path of ['git/id/extra', 'git/id%2Fextra', 'github-app/no-id', 'github-app/0', 'git/'])
    assert.equal(
      (
        await forwardNamedGitWebhook(
          new Request(`http://localhost/api/v1/webhooks/${path}`, { method: 'POST', body: '{}' }),
        )
      ).status,
      404,
    )
  assert.equal(
    (await forwardNamedGitWebhook(new Request('http://localhost/api/v1/webhooks/git/id'))).status,
    405,
  )
  assert.equal(
    (
      await forwardNamedGitWebhook(
        new Request('http://localhost/api/v1/webhooks/git/id', {
          method: 'POST',
          body: 'a'.repeat(512 * 1024 + 1),
        }),
      )
    ).status,
    413,
  )
  assert.equal(called, false)
})
