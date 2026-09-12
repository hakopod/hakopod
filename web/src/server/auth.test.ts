import { test } from 'node:test'
import assert from 'node:assert/strict'
import { authenticatedResponse, oauth, pendingMFA, signIn, signOut } from './auth.ts'
import { openSession, sealSession, sessionToken } from './session.ts'

const origin = 'http://127.0.0.1:4173'
const token = 'hs_unit_test_not_a_real_session_12345678'
const request = (body: Record<string, unknown>, cookie = '') =>
  new Request(`${origin}/session`, {
    method: 'POST',
    headers: { Origin: origin, 'Content-Type': 'application/json', Cookie: cookie },
    body: JSON.stringify(body),
  })
const upstream = () =>
  Response.json({
    token,
    user: { name: 'Unit test', email: 'unit@example.invalid' },
    expires_at: '2030-01-01T00:00:00Z',
  })

test('human sessions are sealed and never exposed in browser JSON or headers', async () => {
  const response = await authenticatedResponse(request({}), upstream())
  const cookie = response.headers.getSetCookie()[0]
  assert.equal(response.status, 200)
  assert.equal((await response.text()).includes(token), false)
  assert.equal(cookie.includes(token), false)
  assert.match(cookie, /HttpOnly.*SameSite=Strict/)
  assert.equal(
    sessionToken(new Request(origin, { headers: { Cookie: cookie.split(';')[0] } })),
    token,
  )
  const machine = await authenticatedResponse(
    request({}),
    Response.json({ token: 'hp_not_for_browser_12345678' }),
  )
  assert.equal(machine.status, 502)
  const malformed = await authenticatedResponse(request({}), Response.json(null))
  assert.equal(malformed.status, 502)
})

test('registration and recovery keep mail tokens out of browser responses', async (t) => {
  const calls: { path: string; body: Record<string, unknown> }[] = []
  const fetch = t.mock.method(globalThis, 'fetch', async (path: string, init: RequestInit = {}) => {
    calls.push({ path: String(path), body: JSON.parse(String(init.body)) })
    return Response.json({ accepted: true, token: 'must-never-reach-browser' }, { status: 202 })
  })
  const registration = {
    action: 'register',
    email: 'new@example.invalid',
    name: 'New person',
    password: 'a long registration password',
  }
  const registered = await signIn(request(registration))
  assert.equal(registered.status, 202)
  assert.deepEqual(await registered.json(), { accepted: true })
  assert.equal(registered.headers.getSetCookie().length, 0)
  assert.match(calls[0].path, /auth\/register$/)
  assert.deepEqual(calls[0].body, {
    email: registration.email,
    name: registration.name,
    password: registration.password,
  })
  assert.equal((await signIn(request({ action: 'forgot', email: registration.email }))).status, 202)
  assert.match(calls[1].path, /auth\/password\/forgot$/)
  fetch.mock.mockImplementation(async () =>
    Response.json({ reset: true, token: 'must-never-reach-browser' }),
  )
  const reset = await signIn(
    request({ action: 'reset', token: 'email-proof', password: 'new account password' }),
  )
  assert.deepEqual(await reset.json(), { reset: true })
  assert.match(reset.headers.getSetCookie()[0], /Max-Age=0/)
})

test('verified registration redirects to onboarding with a sealed session', async (t) => {
  t.mock.method(globalThis, 'fetch', async (url: string, init: RequestInit = {}) => {
    assert.match(String(url), /auth\/register\/verify$/)
    assert.deepEqual(JSON.parse(String(init.body)), { token: 'email-proof' })
    return Response.json({ token, user: { name: 'Verified person' }, onboarding_required: true })
  })
  const verified = await signIn(request({ action: 'verify', token: 'email-proof' }))
  const result = await verified.json()
  assert.equal(result.onboarding_required, true)
  assert.equal(result.token, undefined)
  const redirect = await authenticatedResponse(
    request({}),
    Response.json({ token, onboarding_required: true }),
    true,
  )
  assert.equal(redirect.headers.get('Location'), '/login/onboarding')
})

test('setup forwards only the chosen profile and installer proof; CSRF cannot reach auth', async (t) => {
  const calls: { url: string; headers: Headers; body: Record<string, unknown> }[] = []
  t.mock.method(globalThis, 'fetch', async (url: string, init: RequestInit = {}) => {
    calls.push({
      url: String(url),
      headers: new Headers(init.headers),
      body: JSON.parse(String(init.body)),
    })
    return upstream()
  })
  const chosen = {
    action: 'setup',
    name: 'Chosen owner',
    email: 'chosen@example.invalid',
    password: 'a long unit test password',
    installer_credential: 'installer_unit_test',
  }
  assert.equal((await signIn(request(chosen))).status, 200)
  assert.deepEqual(calls[0].body, {
    name: chosen.name,
    email: chosen.email,
    password: chosen.password,
    setup_token: chosen.installer_credential,
  })
  assert.equal(calls[0].headers.has('Authorization'), false)
  await signIn(request({ ...chosen, installer_credential: 'hp_unit_bootstrap' }))
  assert.equal(calls[1].headers.get('Authorization'), 'Bearer hp_unit_bootstrap')
  assert.equal(calls[1].body.setup_token, '')
  const crossSite = new Request(request(chosen), { headers: { Origin: 'https://other.example' } })
  assert.equal((await signIn(crossSite)).status, 403)
  assert.equal(calls.length, 2)
  assert.equal((await signIn(request({ action: 'login', password: 'x'.repeat(5000) }))).status, 413)
  assert.equal(calls.length, 2)
})

test('OAuth MFA challenges stay in sealed short-lived cookies and complete server-side', async (t) => {
  const challenge = 'unit_test_mfa_challenge_123456789'
  const fetch = t.mock.method(globalThis, 'fetch', async () =>
    Response.json({ mfa_required: true, challenge }),
  )
  const callback = await oauth(
    new Request(`${origin}/api/v1/auth/oauth/github/callback?state=test`),
    'auth/oauth/github/callback',
  )
  assert.equal(callback.status, 303)
  assert.equal(callback.headers.get('Location'), '/?mfa=1')
  const cookie = callback.headers.getSetCookie().find((value) => value.startsWith('hakopod_mfa='))!
  assert.match(cookie, /HttpOnly.*Max-Age=300/)
  assert.equal(cookie.includes(challenge), false)
  assert.equal(openSession(cookie.split(';')[0].slice('hakopod_mfa='.length)), challenge)
  const finish = request({ action: 'mfa', code: '123456' }, cookie.split(';')[0])
  assert.equal(pendingMFA(finish), true)
  fetch.mock.mockImplementation(async (_url: unknown, init: RequestInit = {}) => {
    assert.deepEqual(JSON.parse(String(init.body)), { challenge, code: '123456' })
    return upstream()
  })
  assert.equal((await signIn(finish)).status, 200)
  assert.equal((await signIn(request({ action: 'mfa', code: '123456' }))).status, 401)
})

test('OAuth state is host-only, errors stay private, and sign-out revokes upstream', async (t) => {
  const fetch = t.mock.method(
    globalThis,
    'fetch',
    async () =>
      new Response(null, {
        status: 302,
        headers: {
          Location: 'https://github.com/login/oauth/authorize?state=unit',
          'Set-Cookie': 'hakopod_oauth=unit_state; Path=/; HttpOnly; SameSite=Lax',
        },
      }),
  )
  const response = await oauth(
    new Request(`${origin}/api/v1/auth/oauth/github/start`),
    'auth/oauth/github/start',
  )
  assert.equal(response.status, 302)
  assert.match(
    response.headers.get('Set-Cookie')!,
    /hakopod_oauth=unit_state.*HttpOnly.*SameSite=Lax/,
  )
  assert.equal(response.headers.get('Set-Cookie')!.includes('Domain='), false)
  fetch.mock.mockImplementation(async () =>
    Response.json(
      { error: { code: 'disabled', message: 'Provider unavailable', token } },
      { status: 400 },
    ),
  )
  const denied = await oauth(
    new Request(`${origin}/api/v1/auth/oauth/google/start`),
    'auth/oauth/google/start',
  )
  assert.equal(denied.status, 400)
  assert.equal((await denied.text()).includes(token), false)
  assert.match(denied.headers.get('Cache-Control')!, /no-store/)
  fetch.mock.mockImplementation(async (_url: unknown, init: RequestInit = {}) => {
    assert.equal(new Headers(init.headers).get('Authorization'), `Bearer ${token}`)
    return Response.json({ logged_out: true })
  })
  const logout = await signOut(
    new Request(`${origin}/session`, {
      method: 'DELETE',
      headers: { Origin: origin, Cookie: `hakopod_session=${sealSession(token)}` },
    }),
  )
  assert.equal((await logout.json()).server_session_revoked, true)
  assert.match(logout.headers.get('Set-Cookie')!, /Max-Age=0/)
})
