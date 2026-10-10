import { createFileRoute } from '@tanstack/react-router'
import { proxyExplorer } from '../server/database-explorer'
export const Route = createFileRoute('/synehq/s/$scope/$')({
  server: {
    handlers: {
      GET: proxyExplorer,
      HEAD: proxyExplorer,
      POST: proxyExplorer,
      PUT: proxyExplorer,
      PATCH: proxyExplorer,
      DELETE: proxyExplorer,
    },
  },
})
