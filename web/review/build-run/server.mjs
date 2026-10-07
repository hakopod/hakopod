import { createServer } from 'vite'
import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'
import { realpathSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

// DEVELOPMENT ONLY. Start this bounded preview on the development VM.
const root = fileURLToPath(new URL('.', import.meta.url))
const project = fileURLToPath(new URL('../../../', import.meta.url))
const server = await createServer({
  configFile: false,
  root,
  publicDir: `${project}/web/public`,
  plugins: [react(), tailwindcss()],
  server: {
    host: '127.0.0.1', port: 4203, strictPort: true, hmr: false,
    fs: { allow: [project, realpathSync(`${project}/web/node_modules`)] },
  },
  cacheDir: `${project}/.local/build-run-review/vite`,
  clearScreen: false,
})
await server.listen()
console.log('DEVELOPMENT ONLY build-run review: http://127.0.0.1:4203/builds/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa?fixture=queued')
let stopping = false
async function stop() {
  if (stopping) return
  stopping = true
  await server.close()
  process.exit(0)
}
const timer = setTimeout(stop, 45 * 60 * 1000)
timer.unref()
process.on('SIGINT', stop)
process.on('SIGTERM', stop)
