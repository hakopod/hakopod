import { createFileRoute } from '@tanstack/react-router'
import {
  apiURL,
  boundedBody,
  privateHeaders,
  requireSameOrigin,
  sessionToken,
} from '../server/session'

const allowed =
  /^(me|projects|applications(?:\/[A-Za-z0-9_-]+(?:\/(?:logs|rollback))?)?|plan|deployments(?:\/[A-Za-z0-9_-]+(?:\/cancel)?)?|nodes|keys(?:\/[A-Za-z0-9_-]+(?:\/rotate)?)?|audit)$/

async function proxy({ request, params }: { request: Request; params: { _splat?: string } }) {
  try {
    if (request.method !== 'GET') {
      const rejected = requireSameOrigin(request)
      if (rejected) return rejected
    }
    const path = params._splat || ''
    if (!allowed.test(path))
      return Response.json({ error: { message: 'Unknown API endpoint.' } }, { status: 404 })
    const token = sessionToken(request)
    if (!token)
      return Response.json(
        { error: { code: 'unauthorized', message: 'Sign in with a valid API key to continue.' } },
        { status: 401, headers: privateHeaders },
      )
    const headers = new Headers({ Authorization: `Bearer ${token}`, Accept: 'application/json' })
    const idempotency = request.headers.get('idempotency-key')
    if (idempotency) headers.set('Idempotency-Key', idempotency)
    let body: string | undefined
    if (!['GET', 'HEAD'].includes(request.method)) {
      const payload = await boundedBody(request, 1024 * 1024)
      if (payload === null)
        return Response.json(
          { error: { message: 'Request too large. Maximum size is 1 MiB.' } },
          { status: 413 },
        )
      body = payload
      headers.set('Content-Type', 'application/json')
    }
    const streaming = path.endsWith('/logs')
    const response = await fetch(apiURL(path) + new URL(request.url).search, {
      method: request.method,
      headers,
      body,
      redirect: 'error',
      signal: AbortSignal.any([
        request.signal,
        AbortSignal.timeout(streaming ? 5 * 60 * 1000 : 30000),
      ]),
    })
    return new Response(response.body, {
      status: response.status,
      headers: {
        ...privateHeaders,
        'Content-Type': response.headers.get('content-type') || 'application/json',
        ...(streaming ? { 'X-Accel-Buffering': 'no' } : {}),
      },
    })
  } catch {
    return Response.json(
      {
        error: {
          code: 'api_unavailable',
          message:
            'The management API is unavailable. Existing workloads continue independently; retry when the API is ready.',
        },
      },
      { status: 503, headers: privateHeaders },
    )
  }
}

export const Route = createFileRoute('/api/$')({
  server: { handlers: { GET: proxy, POST: proxy, DELETE: proxy } },
})
