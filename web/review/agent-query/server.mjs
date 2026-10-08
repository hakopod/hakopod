import { createServer } from 'vite'
import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'
import { realpathSync, readdirSync, statSync, readFileSync } from 'node:fs'
import { createHash } from 'node:crypto'
import { gzipSync } from 'node:zlib'
import { fileURLToPath } from 'node:url'

const root = fileURLToPath(new URL('.', import.meta.url))
const project = fileURLToPath(new URL('../../../', import.meta.url))
const port = Number(process.env.HAKOPOD_QUERY_REVIEW_PORT || 4199)
if (!Number.isInteger(port) || port < 1024 || port > 65535) throw new Error('Invalid review port')
const server = await createServer({
  configFile: false,
  root,
  publicDir: `${project}/web/public`,
  plugins: [{ name: 'review-shipped-sql-worker', configureServer(server) {
    if (process.env.HAKOPOD_QUERY_REVIEW_BUILT_WORKER !== '1') return
    const assets = `${project}/web/dist/client/assets`
    const names = readdirSync(assets).filter(name => name.startsWith('sql-parser.worker-') && name.endsWith('.js'))
    const candidates = names.filter(name => statSync(`${assets}/${name}`).size > 1024)
    if (candidates.length !== 1) throw new Error('Built SQL worker is required for this development review')
    const bytes = readFileSync(`${assets}/${candidates[0]}`)
    const compressed = gzipSync(bytes)
    console.log(`Built SQL worker SHA256 ${createHash('sha256').update(bytes).digest('hex')}`)
    server.middlewares.use((req, res, next) => {
      if (req.url?.split('?')[0].endsWith('/sql-parser.worker.ts') && req.url.includes('worker_file')) {
        res.setHeader('Content-Type', 'text/javascript')
        res.setHeader('Cache-Control', 'public, max-age=31536000, immutable')
        res.setHeader('Vary', 'Accept-Encoding')
        if (/(?:^|,)\s*gzip(?:\s*;\s*q=(?!0(?:\.0*)?(?:,|$))[^,]*)?(?:,|$)/i.test(req.headers['accept-encoding'] || '')) {
          res.setHeader('Content-Encoding', 'gzip')
          res.end(compressed)
        } else res.end(bytes)
      } else next()
    })
  } }, react(), tailwindcss()],
  server: { host: '127.0.0.1', port, strictPort: true, hmr: false, fs: { allow: [project, realpathSync(`${project}/web/node_modules`)] } },
  cacheDir: `${project}/.local/agent-query-review/vite`,
  clearScreen: false,
})
await server.listen()
console.log(`Artificial SQL query UI fixture only: http://127.0.0.1:${port}`)
async function stop() { await server.close(); process.exit(0) }
process.on('SIGINT', stop)
process.on('SIGTERM', stop)
