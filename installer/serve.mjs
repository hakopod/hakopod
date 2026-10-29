// Production entrypoint: one process, no request logger (OAuth URLs contain codes).
import { serve } from 'srvx/node'
import { staticMiddleware } from 'srvx/static'
import { fileURLToPath } from 'node:url'
import { AsyncLocalStorage } from 'node:async_hooks'
import application from './dist/server/server.js'

if (!process.env.HAKOPOD_SESSION_SECRET || process.env.HAKOPOD_SESSION_SECRET.length < 32)
  throw new Error('Set HAKOPOD_SESSION_SECRET to at least 32 random characters.')
const port = Number(process.env.PORT || 3000)
if (!Number.isInteger(port) || port < 1 || port > 65535) throw new Error('PORT must be 1–65535.')
const cert = process.env.HAKOPOD_DASHBOARD_TLS_CERT
const key = process.env.HAKOPOD_DASHBOARD_TLS_KEY
if (Boolean(cert) !== Boolean(key)) throw new Error('Dashboard TLS needs a certificate and key.')

// Private management access. Every request is checked against the
// installation access policy before anything renders; Go makes the decision.
// Signed provider webhooks are the only exemption.
const apiOrigin = new URL(process.env.HAKOPOD_API_URL || 'http://127.0.0.1:8080').origin
const webhook = /^\/api\/v1\/webhooks\/(?:github|gitlab|git\/[A-Za-z0-9_-]+|github-app\/[1-9][0-9]*|nodes\/[a-f0-9]{32}\/git\/[a-f0-9]{32})$/
const forwardedHeaders = ['X-Forwarded-For', 'X-Real-IP', 'CF-Connecting-IP']
const peerContext = new AsyncLocalStorage()
const decisions = new Map()

function peerHeaders(request) {
  const headers = { 'X-Hakopod-Peer-IP': request.ip || '' }
  for (const name of forwardedHeaders) {
    const value = request.headers.get(name)
    if (value) headers['X-Hakopod-Peer-' + name] = value.slice(0, 2048)
  }
  return headers
}

async function decide(headers) {
  const key = JSON.stringify(headers)
  const cached = decisions.get(key)
  if (cached && Date.now() - cached.at < 5000) return cached.status
  let status = 503
  try {
    const response = await fetch(apiOrigin + '/internal/access/check', { headers, signal: AbortSignal.timeout(5000) })
    status = response.status
  } catch {}
  if (status === 204 || status === 403) {
    if (decisions.size >= 4096) decisions.clear()
    decisions.set(key, { at: Date.now(), status })
  }
  return status
}

async function privateAccess(request, next) {
  const headers = peerHeaders(request)
  const exempt = request.method === 'POST' && webhook.test(new URL(request.url).pathname)
  if (!exempt) {
    const status = await decide(headers)
    if (status === 403)
      return new Response('This network is not allowed to reach this Hakopod installation.\n', { status: 403, headers: { 'Content-Type': 'text/plain', 'Cache-Control': 'no-store' } })
    if (status !== 204)
      return new Response('The installation access policy could not be checked. Retry, or run `sudo python3 /opt/hakopod/maintenance/maintenance.py --access-reset` on the server.\n', { status: 503, headers: { 'Content-Type': 'text/plain', 'Cache-Control': 'no-store' } })
  }
  return peerContext.run(headers, next)
}

// Every dashboard call to the API inside a request carries that request's
// peer, so Go can key rate limits, audit rows and lockout checks by the real
// client. Only requests to the API origin are touched, and any peer headers a
// handler copied from the browser are replaced.
const nativeFetch = globalThis.fetch
globalThis.fetch = (input, init) => {
  const peer = peerContext.getStore()
  const target = input instanceof Request ? input.url : String(input)
  if (!peer || !URL.canParse(target) || new URL(target).origin !== apiOrigin) return nativeFetch(input, init)
  const headers = new Headers(init?.headers ?? (input instanceof Request ? input.headers : undefined))
  for (const name of [...headers.keys()]) if (name.startsWith('x-hakopod-peer-')) headers.delete(name)
  for (const [name, value] of Object.entries(peer)) headers.set(name, value)
  return nativeFetch(input, { ...init, headers })
}

const server = serve({
  fetch: application.fetch,
  hostname: process.env.HOST || '127.0.0.1',
  port,
  tls: cert ? { cert, key } : undefined,
  gracefulShutdown: true,
  middleware: [privateAccess, staticMiddleware({ dir: fileURLToPath(new URL('./dist/client', import.meta.url)) })],
  node: { maxHeaderSize: 16384, requestTimeout: 30000, headersTimeout: 15000, keepAliveTimeout: 5000 },
  error: () => new Response('The dashboard could not complete this request.', { status: 500 }),
})
await server.ready()
