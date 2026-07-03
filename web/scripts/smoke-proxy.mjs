import { readFile } from 'node:fs/promises'
import assert from 'node:assert/strict'

const origin = new URL(process.env.HAKOPOD_WEB_URL || 'http://127.0.0.1:4173').origin
const request = (path, init = {}) =>
  fetch(origin + path, { ...init, signal: AbortSignal.timeout(10000) })
const unauthorized = await request('/api/me')
assert.equal(unauthorized.status, 401)
const html = await request('/')
assert.equal(html.status, 200)
const source = await html.text()
const stylesheet = source.match(/href="([^\"]+\.css)"/)
assert.ok(stylesheet, 'SSR document includes a stylesheet')
const css = await request(stylesheet[1])
assert.equal(css.status, 200)
assert.match(css.headers.get('content-type'), /text\/css/)

for (const provider of ['github', 'gitlab']) {
  const webhookMethod = await request(`/api/v1/webhooks/${provider}`)
  assert.equal(webhookMethod.status, 405, 'exact webhook route is public and accepts POST only')
}

const crossOrigin = await request('/api/plan', {
  method: 'POST',
  headers: { Origin: 'https://untrusted.example' },
  body: '{}',
})
assert.equal(crossOrigin.status, 403)
const oversizedSignIn = await request('/session', {
  method: 'POST',
  headers: { Origin: origin },
  body: 'x'.repeat(4097),
})
assert.equal(oversizedSignIn.status, 413)
if (process.argv.includes('--public-only')) {
  console.log(
    'PASS: production SSR/static assets; unauthorized access; cross-origin rejection; sign-in body bound. Backend authentication intentionally not exercised.',
  )
  process.exit(0)
}
if (!process.env.HAKOPOD_SMOKE_LOGIN_FILE)
  throw new Error(
    'Set HAKOPOD_SMOKE_LOGIN_FILE to a protected JSON file containing an authorized existing email, password and optional code. This script never creates an owner.',
  )
const credentials = JSON.parse(await readFile(process.env.HAKOPOD_SMOKE_LOGIN_FILE, 'utf8'))
const login = await request('/session', {
  method: 'POST',
  headers: { Origin: origin, 'Content-Type': 'application/json' },
  body: JSON.stringify({
    action: 'login',
    email: credentials.email,
    password: credentials.password,
    code: credentials.code || '',
  }),
})
assert.equal(login.status, 200, 'existing human account signs in')
const loginBody = await login.json()
assert.equal(loginBody.authenticated, true)
assert.equal('token' in loginBody, false, 'the BFF strips the human bearer token')
const setCookie = login.headers
  .getSetCookie()
  .find((value) => /^(?:__Host-)?hakopod_session=/.test(value))
assert.ok(
  setCookie?.includes('HttpOnly') &&
    setCookie.includes('SameSite=Strict') &&
    setCookie.includes('Path=/'),
)
assert.equal(setCookie.includes(credentials.password), false)
if (origin.startsWith('https:')) assert.ok(setCookie.includes('Secure'))
const Cookie = setCookie.split(';')[0]
const me = await request('/api/me', { headers: { Cookie } })
assert.equal(me.status, 200)
const identity = await me.json()
assert.equal(identity.credential_type, 'browser')
assert.equal('token' in identity, false)
const csrf = await request('/api/plan', {
  method: 'POST',
  headers: { Cookie, Origin: 'https://untrusted.example' },
  body: '{}',
})
assert.equal(csrf.status, 403)
const unknown = await request('/api/not-a-platform-endpoint', { headers: { Cookie } })
assert.equal(unknown.status, 404)
const oversized = await request('/api/plan', {
  method: 'POST',
  headers: { Cookie, Origin: origin },
  body: 'x'.repeat(1024 * 1024 + 1),
})
assert.equal(oversized.status, 413)
const logout = await request('/session', { method: 'DELETE', headers: { Cookie, Origin: origin } })
assert.equal(logout.status, 200)
assert.match(logout.headers.get('set-cookie'), /Max-Age=0/)
assert.equal(
  (await request('/api/me', { headers: { Cookie } })).status,
  401,
  'Go revoked the underlying session',
)
console.log(
  'PASS: production SSR/assets; unauthorized access; encrypted cookie login; API proxy; CSRF; path allowlist; 1 MiB body bound; logout.',
)
