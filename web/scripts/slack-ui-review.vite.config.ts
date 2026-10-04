import { fileURLToPath } from 'node:url'
import { resolve } from 'node:path'
import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'

const dashboardRoot = fileURLToPath(new URL('../', import.meta.url))
const hatchUIRoot = fileURLToPath(new URL('../../packages/ui/packages/ui', import.meta.url))
const cloudDashboardRoot = process.env.HAKOPOD_CLOUD_DASHBOARD_ROOT
  ? resolve(process.env.HAKOPOD_CLOUD_DASHBOARD_ROOT)
  : undefined
const publicEdition = resolve(dashboardRoot, 'src/lib/dashboard-edition.tsx')
const cloudEdition = cloudDashboardRoot
  ? resolve(cloudDashboardRoot, 'src/lib/dashboard-edition.tsx')
  : undefined

// This server exists solely for the visual-review fixture. It deliberately
// omits TanStack Start so a static HTML fixture remains a client-only route.
export default defineConfig({
  root: dashboardRoot,
  resolve: {
    alias: [
      ...(cloudEdition
        ? [
            { find: '../lib/dashboard-edition', replacement: cloudEdition },
            { find: publicEdition, replacement: cloudEdition },
          ]
        : []),
      { find: '@', replacement: fileURLToPath(new URL('../src', import.meta.url)) },
    ],
  },
  plugins: [tailwindcss(), react()],
  server: {
    host: '127.0.0.1',
    port: 14595,
    strictPort: true,
    fs: {
      allow: [dashboardRoot, hatchUIRoot, ...(cloudDashboardRoot ? [cloudDashboardRoot] : [])],
    },
  },
})
