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
const key =
  process.env.HAKOPOD_API_KEY ||
  (process.env.HAKOPOD_API_KEY_FILE
    ? (await readFile(process.env.HAKOPOD_API_KEY_FILE, 'utf8')).trim()
    : '')
if (!key) throw new Error('Set HAKOPOD_API_KEY_FILE or HAKOPOD_API_KEY. Values are never printed.')
const login = await request('/session', {
  method: 'POST',
  headers: { Origin: origin, 'Content-Type': 'application/json' },
  body: JSON.stringify({ token: key }),
})
assert.equal(login.status, 200, 'valid key signs in')
const setCookie = login.headers.get('set-cookie')
assert.ok(
  setCookie?.includes('HttpOnly') &&
    setCookie.includes('SameSite=Strict') &&
    setCookie.includes('Path=/'),
)
assert.equal(setCookie.includes(key), false, 'bearer token is not in cleartext cookie')
if (origin.startsWith('https:')) assert.ok(setCookie.includes('Secure'))
const Cookie = setCookie.split(';')[0]
const me = await request('/api/me', { headers: { Cookie } })
assert.equal(me.status, 200)
assert.equal((await me.text()).includes(key), false)
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
console.log(
  'PASS: production SSR/assets; unauthorized access; encrypted cookie login; API proxy; CSRF; path allowlist; 1 MiB body bound; logout.',
)
