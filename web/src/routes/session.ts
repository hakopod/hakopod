import { createFileRoute } from '@tanstack/react-router'
import {
  apiURL,
  boundedBody,
  privateHeaders,
  requireSameOrigin,
  sealSession,
  sessionCookie,
} from '../server/session'

export const Route = createFileRoute('/session')({
  server: {
    handlers: {
      POST: async ({ request }) => {
        try {
          const rejected = requireSameOrigin(request)
          if (rejected) return rejected
          const body = await boundedBody(request, 4096)
          if (body === null)
            return Response.json({ error: { message: 'API key is too long.' } }, { status: 413 })
          let input: unknown
          try {
            input = JSON.parse(body)
          } catch {
            return Response.json(
              { error: { message: 'Expected a JSON request body.' } },
              { status: 400 },
            )
          }
          const token =
            input && typeof input === 'object' && 'token' in input ? input.token : undefined
          if (
            typeof token !== 'string' ||
            token.length < 16 ||
            token.length > 512 ||
            /\s/.test(token)
          )
            return Response.json({ error: { message: 'Enter a valid API key.' } }, { status: 400 })
          const response = await fetch(apiURL('me'), {
            headers: { Authorization: `Bearer ${token}` },
            signal: AbortSignal.timeout(10000),
            redirect: 'error',
          })
          if (!response.ok)
            return new Response(await response.text(), {
              status: response.status,
              headers: { ...privateHeaders, 'Content-Type': 'application/json' },
            })
          return Response.json(
            { authenticated: true },
            {
              headers: {
                ...privateHeaders,
                'Set-Cookie': sessionCookie(request, sealSession(token)),
              },
            },
          )
        } catch {
          return Response.json(
            {
              error: {
                code: 'connection_failed',
                message:
                  'Cannot connect securely to the management API. Check its address, readiness, and dashboard session configuration.',
              },
            },
            { status: 503, headers: privateHeaders },
          )
        }
      },
      DELETE: ({ request }) => {
        const rejected = requireSameOrigin(request)
        if (rejected) return rejected
        return Response.json(
          { authenticated: false },
          { headers: { ...privateHeaders, 'Set-Cookie': sessionCookie(request, '', true) } },
        )
      },
    },
  },
})
