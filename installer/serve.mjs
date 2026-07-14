// Production entrypoint: one process, no request logger (OAuth URLs contain codes).
import { serve } from 'srvx/node'
import { staticMiddleware } from 'srvx/static'
import { fileURLToPath } from 'node:url'
import application from './dist/server/server.js'

if (!process.env.HAKOPOD_SESSION_SECRET || process.env.HAKOPOD_SESSION_SECRET.length < 32)
  throw new Error('Set HAKOPOD_SESSION_SECRET to at least 32 random characters.')
const port = Number(process.env.PORT || 3000)
if (!Number.isInteger(port) || port < 1 || port > 65535) throw new Error('PORT must be 1–65535.')
const cert = process.env.HAKOPOD_DASHBOARD_TLS_CERT
const key = process.env.HAKOPOD_DASHBOARD_TLS_KEY
if (Boolean(cert) !== Boolean(key)) throw new Error('Dashboard TLS needs a certificate and key.')
const server = serve({
  fetch: application.fetch,
  hostname: process.env.HOST || '127.0.0.1',
  port,
  tls: cert ? { cert, key } : undefined,
  gracefulShutdown: true,
  middleware: [staticMiddleware({ dir: fileURLToPath(new URL('./dist/client', import.meta.url)) })],
  node: { maxHeaderSize: 16384, requestTimeout: 30000, headersTimeout: 15000, keepAliveTimeout: 5000 },
  error: () => new Response('The dashboard could not complete this request.', { status: 500 }),
})
await server.ready()
