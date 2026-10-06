import { createServer } from 'vite'
import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'
import { fileURLToPath } from 'node:url'

const root = fileURLToPath(new URL('.', import.meta.url))
const project = fileURLToPath(new URL('../../../', import.meta.url))
const port = Number(process.env.HAKOPOD_BINDING_REVIEW_PORT || 4198)
if (!Number.isInteger(port) || port < 1024 || port > 65535) throw new Error('Invalid review port')
const server = await createServer({
  configFile: false,
  root,
  publicDir: `${project}/web/public`,
  plugins: [react(), tailwindcss()],
  server: { host: '127.0.0.1', port, strictPort: true, hmr: false, fs: { allow: [project] } },
  cacheDir: `${project}/.local/managed-binding-review/vite`,
  clearScreen: false,
})
await server.listen()
console.log(`Artificial managed binding UI fixture only: http://127.0.0.1:${port}`)
async function stop() { await server.close(); process.exit(0) }
process.on('SIGINT', stop)
process.on('SIGTERM', stop)
