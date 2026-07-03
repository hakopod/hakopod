import { fileURLToPath } from 'node:url'
import { serve } from 'srvx'
import { staticMiddleware } from 'srvx/static'
import application from '../dist/server/server.js'

if (!process.env.HAKOPOD_SESSION_SECRET || process.env.HAKOPOD_SESSION_SECRET.length < 32)
  throw new Error('Set HAKOPOD_SESSION_SECRET to at least 32 random characters.')
const port = Number(process.env.PORT || '4173')
if (!Number.isInteger(port) || port < 1 || port > 65535) throw new Error('PORT must be 1–65535.')
// No per-request logger: OAuth callback query strings can contain credentials.
serve({
  hostname: process.env.HOST || '127.0.0.1',
  port,
  fetch: application.fetch,
  gracefulShutdown: true,
  middleware: [
    staticMiddleware({ dir: fileURLToPath(new URL('../dist/client/', import.meta.url)) }),
  ],
})
