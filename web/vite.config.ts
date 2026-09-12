import { defineConfig, loadEnv } from 'vite'
import { tanstackStart } from '@tanstack/react-start/plugin/vite'
import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'

export default defineConfig(({ mode }) => {
  // HAKOPOD_ variables stay server-side; only VITE_ variables are exposed by Vite.
  const settings = loadEnv(mode, process.cwd(), 'HAKOPOD_')
  for (const [name, value] of Object.entries(settings))
    if (!process.env[name]) process.env[name] = value
  return {
    plugins: [tailwindcss(), tanstackStart(), react()],
    // The local design system exports TSX source; production Node runs only JS.
    ssr: { noExternal: ['@hakopod/hatch-ui'] },
    server: { port: 3000, host: '127.0.0.1' },
    build: { sourcemap: false, chunkSizeWarningLimit: 450 },
  }
})
