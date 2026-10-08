import { createServer } from 'vite'
import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'
import { realpathSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

const root = fileURLToPath(new URL('.', import.meta.url))
const project = fileURLToPath(new URL('../../../', import.meta.url))
const port = Number(process.env.HAKOPOD_SCOPE_REVIEW_PORT || 4211)
if (!Number.isInteger(port) || port < 1024 || port > 65535) throw new Error('Invalid review port')
const server = await createServer({
  configFile: false,
  root,
  publicDir: `${project}/web/public`,
  plugins: [react(), tailwindcss()],
  server: { host: '127.0.0.1', port, strictPort: true, hmr: false, fs: { allow: [project, realpathSync(`${project}/web/node_modules`)] } },
  cacheDir: `${project}/.local/agent-scope-review/vite`,
  clearScreen: false,
})
await server.listen()
console.log(`Artificial scope deletion UI fixture only: http://127.0.0.1:${port}`)
async function stop() { await server.close(); process.exit(0) }
process.on('SIGINT', stop)
process.on('SIGTERM', stop)
