import { test } from 'node:test'
import assert from 'node:assert/strict'
import {
  sealSession,
  openSession,
  sessionCookie,
  requireSameOrigin,
  boundedBody,
  apiURL,
} from './session.ts'

test('encrypted sessions expire and reject tampering', () => {
  const sealed = sealSession('hp_test_do_not_use', 1000)
  assert.equal(sealed.includes('hp_test'), false)
  assert.equal(openSession(sealed, 2000), 'hp_test_do_not_use')
  assert.equal(openSession(sealed, 1000 + 12 * 60 * 60 * 1000), null)
  const tampered = Buffer.from(sealed, 'base64url')
  tampered[30] ^= 1
  assert.equal(openSession(tampered.toString('base64url'), 2000), null)
})

test('cookies are host-only and mutation origin is checked', () => {
  process.env.HAKOPOD_WEB_ORIGIN = 'https://console.example.com'
  const req = new Request('http://internal:3000/session', {
    method: 'POST',
    headers: { origin: 'https://console.example.com' },
  })
  assert.match(sessionCookie(req, 'encrypted'), /^__Host-hakopod_session=/)
  assert.match(sessionCookie(req, 'encrypted'), /HttpOnly.*SameSite=Strict.*Secure/)
  assert.equal(requireSameOrigin(req), undefined)
  assert.equal(
    requireSameOrigin(new Request(req, { headers: { origin: 'https://app.example.com' } }))?.status,
    403,
  )
  delete process.env.HAKOPOD_WEB_ORIGIN
})

test('chunked request bodies cannot bypass the memory bound', async () => {
  let cancelled = false
  const body = new ReadableStream({
    start(controller) {
      controller.enqueue(new Uint8Array(20))
      controller.enqueue(new Uint8Array(20))
    },
    cancel() {
      cancelled = true
    },
  })
  const request = new Request('http://127.0.0.1/session', {
    method: 'POST',
    body,
    duplex: 'half',
  } as RequestInit)
  assert.equal(await boundedBody(request, 32), null)
  assert.equal(cancelled, true)
  assert.equal(
    await boundedBody(
      new Request('http://127.0.0.1/session', { method: 'POST', body: '{"ok":true}' }),
      32,
    ),
    '{"ok":true}',
  )
})

test('bearer credentials only leave the server over verified HTTPS or loopback', () => {
  const previous = process.env.HAKOPOD_API_URL
  try {
    for (const value of [
      'http://remote.example',
      'https://user:password@api.example',
      'https://api.example/?query=1',
      'https://api.example/#fragment',
      'https://api.example/unexpected',
      'file:///tmp/api',
    ]) {
      process.env.HAKOPOD_API_URL = value
      assert.throws(() => apiURL('me'))
    }
    process.env.HAKOPOD_API_URL = 'https://api.example/api/v1/'
    assert.equal(apiURL('me'), 'https://api.example/api/v1/me')
    process.env.HAKOPOD_API_URL = 'http://127.0.0.1:8080'
    assert.equal(apiURL('me'), 'http://127.0.0.1:8080/api/v1/me')
    process.env.HAKOPOD_API_URL = 'http://[::1]:8080'
    assert.equal(apiURL('me'), 'http://[::1]:8080/api/v1/me')
  } finally {
    if (previous === undefined) delete process.env.HAKOPOD_API_URL
    else process.env.HAKOPOD_API_URL = previous
  }
})
