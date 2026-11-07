import { createFileRoute } from '@tanstack/react-router'
import { forwardOAuthMetadata } from '../server/mcp'

export const Route = createFileRoute('/.well-known/$')({
  server: { handlers: { GET: ({ request }) => forwardOAuthMetadata(request) } },
})
