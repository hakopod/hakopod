import { fileURLToPath } from 'node:url'
import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'

const dashboardRoot = fileURLToPath(new URL('../', import.meta.url))
const hatchUIRoot = fileURLToPath(new URL('../../packages/ui/packages/ui', import.meta.url))
const templatesRoot = fileURLToPath(new URL('../../templates', import.meta.url))

// Client-only server for the marketing screenshot fixture. Like the Slack review
// fixture it omits TanStack Start, so the static HTML entry is never a product route.
export default defineConfig({
  root: dashboardRoot,
  resolve: { alias: { '@': fileURLToPath(new URL('../src', import.meta.url)) } },
  plugins: [tailwindcss(), react()],
  server: {
    host: '127.0.0.1',
    port: 14596,
    strictPort: true,
    fs: { allow: [dashboardRoot, hatchUIRoot, templatesRoot] },
  },
})
