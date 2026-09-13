import { createFileRoute } from '@tanstack/react-router'
import { proxy } from '../server/api-proxy'

export const Route = createFileRoute('/api/$')({
  server: { handlers: { GET: proxy, POST: proxy, DELETE: proxy, PATCH: proxy, PUT: proxy } },
})
