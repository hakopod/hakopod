import {
  apiURL,
  boundedBody,
  openSession,
  privateHeaders,
  requireSameOrigin,
  sealSession,
  sessionCookie,
  sessionToken,
  trustedOrigin,
} from './session.ts'

const failure = (status: number, message: string, code = 'invalid_request') =>
  Response.json({ error: { code, message } }, { status, headers: privateHeaders })

function upstreamFailure(status: number, value: unknown) {
  const error = value && typeof value === 'object' ? (value as Record<string, unknown>) : {}
  return failure(
    status,
    typeof error.message === 'string' ? error.message.slice(0, 2000) : 'Unable to authenticate.',
    typeof error.code === 'string' ? error.code.slice(0, 100) : 'authentication_failed',
  )
}

function temporaryCookie(request: Request, name: string, value: string, lax = false) {
  const secure = trustedOrigin(request).startsWith('https://')
  return `${secure ? '__Host-' : ''}${name}=${value}; Path=/; HttpOnly; SameSite=${lax ? 'Lax' : 'Strict'}; Max-Age=${value ? 300 : 0}${secure ? '; Secure' : ''}`
}
function cookieValue(request: Request, name: string) {
  const prefix = `${trustedOrigin(request).startsWith('https://') ? '__Host-' : ''}${name}=`
  return (
    request.headers
      .get('cookie')
      ?.split(';')
      .map((item) => item.trim())
      .find((item) => item.startsWith(prefix))
      ?.slice(prefix.length) || ''
  )
}
export function pendingMFA(request: Request) {
  return Boolean(openSession(cookieValue(request, 'hakopod_mfa')))
}

// Go owns authentication and revocation. Its opaque human token is sealed in a
// host cookie and never returned in browser JSON or placed in browser storage.
export async function authenticatedResponse(
  request: Request,
  response: Response,
  redirect = false,
) {
  const text = await boundedBody(response, 64 * 1024)
  if (text === null) return failure(502, 'The authentication response exceeded its size limit.')
  let result: Record<string, unknown>
  try {
    result = JSON.parse(text)
  } catch {
    return failure(502, 'The authentication service returned an invalid response.')
  }
  if (!result || typeof result !== 'object' || Array.isArray(result))
    return failure(502, 'The authentication service returned an invalid response.')
  if (!response.ok) return upstreamFailure(response.status, result.error)
  if (typeof result.token !== 'string' || !/^hs_[A-Za-z0-9_-]{16,509}$/.test(result.token))
    return failure(502, 'The authentication service did not return a valid human session.')
  const headers = new Headers({
    ...privateHeaders,
    'Set-Cookie': sessionCookie(request, sealSession(result.token)),
  })
  headers.append('Set-Cookie', temporaryCookie(request, 'hakopod_mfa', ''))
  if (redirect) headers.set('Location', '/')
  return redirect
    ? new Response(null, { status: 303, headers })
    : Response.json(
        { authenticated: true, user: result.user, expires_at: result.expires_at },
        { headers },
      )
}

export async function signIn(request: Request) {
  try {
    const rejected = requireSameOrigin(request)
    if (rejected) return rejected
    const body = await boundedBody(request, 4096)
    if (body === null) return failure(413, 'The sign-in request is too large.')
    let input: Record<string, unknown>
    try {
      input = JSON.parse(body)
    } catch {
      return failure(400, 'Expected a JSON request body.')
    }
    if (!input || typeof input !== 'object' || Array.isArray(input))
      return failure(400, 'Expected a sign-in request.')
    const action = input.action
    if (!['login', 'setup', 'invite', 'mfa'].includes(String(action)))
      return failure(400, 'Choose a supported sign-in method.')
    const headers = new Headers({ 'Content-Type': 'application/json', Accept: 'application/json' })
    let payload: Record<string, unknown>
    let path: string
    if (action === 'login') {
      path = 'auth/login'
      payload = { email: input.email, password: input.password, code: input.code || '' }
    } else if (action === 'setup') {
      path = 'auth/setup'
      const installer =
        typeof input.installer_credential === 'string' ? input.installer_credential.trim() : ''
      const existing = sessionToken(request)
      const bootstrap = installer.startsWith('hp_')
        ? installer
        : existing?.startsWith('hp_')
          ? existing
          : ''
      if (bootstrap) headers.set('Authorization', `Bearer ${bootstrap}`)
      payload = {
        name: input.name,
        email: input.email,
        password: input.password,
        setup_token: bootstrap ? '' : installer,
      }
    } else if (action === 'mfa') {
      path = 'auth/mfa/complete'
      const challenge = openSession(cookieValue(request, 'hakopod_mfa'))
      if (!challenge) return failure(401, 'This sign-in challenge expired. Start sign-in again.')
      payload = { challenge, code: input.code }
    } else {
      path = 'auth/invites/accept'
      const existing = sessionToken(request)
      if (existing) headers.set('Authorization', `Bearer ${existing}`)
      payload = { token: input.invite_token, name: input.name, password: input.password }
    }
    const response = await fetch(apiURL(path), {
      method: 'POST',
      headers,
      body: JSON.stringify(payload),
      redirect: 'error',
      signal: AbortSignal.any([request.signal, AbortSignal.timeout(15000)]),
    })
    return await authenticatedResponse(request, response)
  } catch {
    return failure(
      503,
      'Cannot connect securely to the authentication service. Try again when the management API is ready.',
      'connection_failed',
    )
  }
}

export async function oauth(request: Request, path: string) {
  const match = path.match(/^auth\/oauth\/(github|google)\/(start|callback)$/)
  if (!match || request.method !== 'GET') return failure(404, 'Unknown sign-in endpoint.')
  try {
    const state = cookieValue(request, 'hakopod_oauth')
    const response = await fetch(apiURL(path) + new URL(request.url).search, {
      headers: state ? { Cookie: `hakopod_oauth=${state}` } : {},
      redirect: 'manual',
      signal: AbortSignal.any([request.signal, AbortSignal.timeout(15000)]),
    })
    if (match[2] === 'start') {
      const location = response.headers.get('location')
      if (!location || ![302, 303, 307].includes(response.status)) {
        const body = await boundedBody(response, 64 * 1024)
        if (body === null)
          return failure(502, 'The identity provider response exceeded its size limit.')
        try {
          return upstreamFailure(response.ok ? 502 : response.status, JSON.parse(body)?.error)
        } catch {
          return failure(502, 'The identity provider did not initialize securely.')
        }
      }
      if (new URL(location).protocol !== 'https:')
        return failure(502, 'The identity provider did not return a secure redirect.')
      const stateCookie = response.headers
        .getSetCookie()
        .find((item) => item.startsWith('hakopod_oauth='))
      const value = stateCookie?.split(';')[0].slice('hakopod_oauth='.length)
      if (!value || value.length > 1024)
        return failure(502, 'The identity provider did not initialize securely.')
      return new Response(null, {
        status: 302,
        headers: {
          ...privateHeaders,
          Location: location,
          'Set-Cookie': temporaryCookie(request, 'hakopod_oauth', value, true),
        },
      })
    }
    const body = await boundedBody(response, 64 * 1024)
    if (body === null) return failure(502, 'The authentication response exceeded its size limit.')
    let result: Record<string, unknown>
    try {
      result = JSON.parse(body)
    } catch {
      return failure(502, 'The identity provider returned an invalid response.')
    }
    if (!result || typeof result !== 'object' || Array.isArray(result))
      return failure(502, 'The identity provider returned an invalid response.')
    if (
      response.ok &&
      result.mfa_required === true &&
      typeof result.challenge === 'string' &&
      result.challenge.length > 16 &&
      result.challenge.length <= 512
    ) {
      const headers = new Headers({ ...privateHeaders, Location: '/?mfa=1' })
      headers.append('Set-Cookie', temporaryCookie(request, 'hakopod_oauth', '', true))
      headers.append(
        'Set-Cookie',
        temporaryCookie(request, 'hakopod_mfa', sealSession(result.challenge)),
      )
      return new Response(null, { status: 303, headers })
    }
    const authenticated = await authenticatedResponse(
      request,
      new Response(body, {
        status: response.status,
        headers: { 'Content-Type': 'application/json' },
      }),
      true,
    )
    authenticated.headers.append('Set-Cookie', temporaryCookie(request, 'hakopod_oauth', '', true))
    return authenticated
  } catch {
    return failure(
      503,
      'The identity provider sign-in could not be completed. Try again.',
      'connection_failed',
    )
  }
}

export async function signOut(request: Request) {
  const rejected = requireSameOrigin(request)
  if (rejected) return rejected
  const token = sessionToken(request)
  let revoked = true
  if (token?.startsWith('hs_')) {
    try {
      const response = await fetch(apiURL('auth/logout'), {
        method: 'POST',
        headers: { Authorization: `Bearer ${token}`, 'Content-Type': 'application/json' },
        body: '{}',
        redirect: 'error',
        signal: AbortSignal.timeout(10000),
      })
      revoked = response.ok || response.status === 401
    } catch {
      revoked = false
    }
  }
  return Response.json(
    { authenticated: false, server_session_revoked: revoked },
    { headers: { ...privateHeaders, 'Set-Cookie': sessionCookie(request, '', true) } },
  )
}
