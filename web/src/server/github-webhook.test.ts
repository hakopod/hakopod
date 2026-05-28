import { test } from 'node:test'
import assert from 'node:assert/strict'
import { createHmac } from 'node:crypto'
import { forwardGitHubWebhook } from './github-webhook.ts'

test('public GitHub forwarding preserves signed bytes and excludes browser authority', async (t) => {
  const bytes = new Uint8Array([123, 32, 34, 120, 34, 58, 34, 195, 169, 34, 32, 125, 10])
  const signature =
    'sha256=' + createHmac('sha256', 'unit_test_signing_secret').update(bytes).digest('hex')
  const calls: RequestInit[] = []
  t.mock.method(globalThis, 'fetch', async (url: unknown, init: RequestInit = {}) => {
    assert.equal(String(url).endsWith('/api/v1/webhooks/github'), true)
    calls.push(init)
    assert.deepEqual(init.body, bytes)
    const headers = new Headers(init.headers)
    assert.equal(headers.get('X-Hub-Signature-256'), signature)
    assert.equal(headers.get('X-GitHub-Event'), 'workflow_run')
    assert.equal(headers.get('X-GitHub-Delivery'), 'unit-delivery')
    for (const header of ['Authorization', 'Cookie', 'Origin', 'X-Untrusted'])
      assert.equal(headers.has(header), false)
    return Response.json(
      { accepted: true },
      { status: 202, headers: { 'Set-Cookie': 'must-not-forward=1' } },
    )
  })
  const response = await forwardGitHubWebhook(
    new Request('http://127.0.0.1:4173/api/v1/webhooks/github', {
      method: 'POST',
      headers: {
        Origin: 'https://github.com',
        Cookie: 'browser-session=unit',
        Authorization: 'Bearer unit',
        'X-Untrusted': 'unit',
        'X-Hub-Signature-256': signature,
        'X-GitHub-Event': 'workflow_run',
        'X-GitHub-Delivery': 'unit-delivery',
      },
      body: bytes,
    }),
  )
  assert.equal(response.status, 202)
  assert.equal(response.headers.has('Set-Cookie'), false)
  assert.match(response.headers.get('Cache-Control')!, /no-store/)
  assert.equal(calls.length, 1)
})

test('webhook exception has an exact path, POST method and bounded raw body', async (t) => {
  let called = false
  t.mock.method(globalThis, 'fetch', async () => {
    called = true
    return Response.json({})
  })
  assert.equal(
    (await forwardGitHubWebhook(new Request('http://127.0.0.1/api/v1/webhooks/github'))).status,
    405,
  )
  assert.equal(
    (
      await forwardGitHubWebhook(
        new Request('http://127.0.0.1/api/v1/webhooks/github/other', {
          method: 'POST',
          body: '{}',
        }),
      )
    ).status,
    404,
  )
  let cancelled = false
  const stream = new ReadableStream({
    start(controller) {
      controller.enqueue(new Uint8Array(512 * 1024))
      controller.enqueue(new Uint8Array(1))
    },
    cancel() {
      cancelled = true
    },
  })
  const request = new Request('http://127.0.0.1/api/v1/webhooks/github', {
    method: 'POST',
    body: stream,
    duplex: 'half',
  } as RequestInit)
  assert.equal((await forwardGitHubWebhook(request)).status, 413)
  assert.equal(cancelled, true)
  assert.equal(called, false)
})
