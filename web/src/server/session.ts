import { createCipheriv, createDecipheriv, createHash, randomBytes } from 'node:crypto'

const devSecret = randomBytes(48).toString('base64url')
const ttl = 12 * 60 * 60

function key() {
  const secret = process.env.HAKOPOD_SESSION_SECRET
  if (process.env.NODE_ENV === 'production' && (!secret || secret.length < 32)) {
    throw new Error(
      'Set HAKOPOD_SESSION_SECRET to at least 32 random characters before starting the dashboard.',
    )
  }
  return createHash('sha256')
    .update(secret || devSecret)
    .digest()
}

export function sealSession(token: string, now = Date.now()) {
  const nonce = randomBytes(12)
  const cipher = createCipheriv('aes-256-gcm', key(), nonce)
  const ciphertext = Buffer.concat([
    cipher.update(JSON.stringify({ token, expires: now + ttl * 1000 }), 'utf8'),
    cipher.final(),
  ])
  return Buffer.concat([nonce, cipher.getAuthTag(), ciphertext]).toString('base64url')
}

export function openSession(value: string, now = Date.now()): string | null {
  if (value.length > 4096) return null
  try {
    const bytes = Buffer.from(value, 'base64url')
    const decipher = createDecipheriv('aes-256-gcm', key(), bytes.subarray(0, 12))
    decipher.setAuthTag(bytes.subarray(12, 28))
    const session = JSON.parse(
      Buffer.concat([decipher.update(bytes.subarray(28)), decipher.final()]).toString('utf8'),
    )
    return typeof session.token === 'string' && session.expires > now ? session.token : null
  } catch {
    return null
  }
}

export function trustedOrigin(request: Request): string {
  const configured = process.env.HAKOPOD_WEB_ORIGIN
  if (configured) return new URL(configured).origin
  const url = new URL(request.url)
  if (
    process.env.NODE_ENV === 'production' &&
    !['localhost', '127.0.0.1', '[::1]'].includes(url.hostname)
  ) {
    throw new Error('Set HAKOPOD_WEB_ORIGIN to the HTTPS dashboard origin.')
  }
  return url.origin
}

export function requireSameOrigin(request: Request) {
  const origin = trustedOrigin(request)
  if (
    request.headers.get('origin') !== origin ||
    request.headers.get('sec-fetch-site') === 'cross-site'
  ) {
    return Response.json(
      {
        error: {
          code: 'origin_denied',
          message: 'This request must originate from the dashboard.',
        },
      },
      { status: 403 },
    )
  }
  if (
    !origin.startsWith('https://') &&
    !['localhost', '127.0.0.1', '[::1]'].includes(new URL(origin).hostname)
  ) {
    return Response.json(
      {
        error: {
          code: 'https_required',
          message: 'Use HTTPS to sign in and manage this installation.',
        },
      },
      { status: 400 },
    )
  }
}

function cookieName(request: Request) {
  return trustedOrigin(request).startsWith('https://')
    ? '__Host-hakopod_session'
    : 'hakopod_session'
}

export function sessionCookie(request: Request, value: string, clear = false) {
  const secure = trustedOrigin(request).startsWith('https://') ? '; Secure' : ''
  return `${cookieName(request)}=${value}; HttpOnly; Path=/; SameSite=Strict; Max-Age=${clear ? 0 : ttl}${secure}`
}

export function sessionToken(request: Request) {
  const prefix = `${cookieName(request)}=`
  const cookie = request.headers
    .get('cookie')
    ?.split(';')
    .map((x) => x.trim())
    .find((x) => x.startsWith(prefix))
  return cookie ? openSession(cookie.slice(prefix.length)) : null
}

export function apiURL(path: string) {
  const url = new URL(process.env.HAKOPOD_API_URL || 'http://127.0.0.1:8080')
  const loopback = ['localhost', '127.0.0.1', '[::1]'].includes(url.hostname)
  if (
    url.username ||
    url.password ||
    url.search ||
    url.hash ||
    !['', '/', '/api/v1', '/api/v1/'].includes(url.pathname)
  ) {
    throw new Error(
      'HAKOPOD_API_URL must be an origin, optionally ending in /api/v1, without credentials, query, or fragment.',
    )
  }
  if (url.protocol !== 'https:' && !(url.protocol === 'http:' && loopback)) {
    throw new Error('The management API requires verified HTTPS except on loopback.')
  }
  return `${url.origin}/api/v1/${path}`
}

export async function boundedBytes(
  request: Pick<Request, 'headers' | 'body'>,
  limit: number,
): Promise<Uint8Array<ArrayBuffer> | null> {
  if (Number(request.headers.get('content-length')) > limit) return null
  if (!request.body) return new Uint8Array(0)
  const reader = request.body.getReader()
  const bytes = new Uint8Array(limit)
  let size = 0
  try {
    while (true) {
      const { done, value } = await reader.read()
      if (done) break
      if (size + value.byteLength > limit) {
        await reader.cancel()
        return null
      }
      bytes.set(value, size)
      size += value.byteLength
    }
    return bytes.subarray(0, size)
  } finally {
    reader.releaseLock()
  }
}

export async function boundedBody(
  request: Pick<Request, 'headers' | 'body'>,
  limit: number,
): Promise<string | null> {
  const bytes = await boundedBytes(request, limit)
  return bytes === null ? null : new TextDecoder().decode(bytes)
}

export const privateHeaders = {
  'Cache-Control': 'no-store',
  'X-Content-Type-Options': 'nosniff',
  'Referrer-Policy': 'no-referrer',
}
