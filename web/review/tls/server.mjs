import { createServer } from 'vite'
import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'
import { fileURLToPath } from 'node:url'

const root = fileURLToPath(new URL('.', import.meta.url))
const project = fileURLToPath(new URL('../../../', import.meta.url))
const server = await createServer({
  configFile: false,
  root,
  publicDir: `${project}/web/public`,
  plugins: [react(), tailwindcss()],
  server: {
    host: '127.0.0.1',
    port: 4199,
    strictPort: true,
    hmr: false,
    fs: { allow: [project] },
  },
  cacheDir: `${project}/.local/tls-review-vite`,
  clearScreen: false,
})
await server.listen()
console.log('Synthetic TLS UI fixture only: http://127.0.0.1:4199')
async function stop() {
  await server.close()
  process.exit(0)
}
process.on('SIGINT', stop)
process.on('SIGTERM', stop)
