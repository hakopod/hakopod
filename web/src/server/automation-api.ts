import { apiURL, boundedBytes, privateHeaders } from './session.ts'
import { editionRequestError } from './dashboard-edition.ts'

// Public machine API: keep browser cookie authentication on /api/* separate.
// Canonical Go handlers enforce key expiry, scope, permissions and rate limits.
const routes: [RegExp, string[]][] = [
  [/^applications$/, ['GET']],
  [/^applications\/[A-Za-z0-9_-]+$/, ['GET']],
  [/^applications\/[A-Za-z0-9_-]+\/provenance$/, ['GET']],
  [/^plan$/, ['POST']],
  [/^deployments$/, ['POST']],
  [/^deployments\/[A-Za-z0-9_-]+$/, ['GET']],
  [/^idempotency\/[A-Za-z0-9_.:-]+$/, ['GET']],
  [/^openapi\.json$/, ['GET']],
]

export async function forwardAutomationAPI(request: Request) {
  const url = new URL(request.url)
  const path = url.pathname.slice('/api/v1/'.length)
  const route = url.pathname.startsWith('/api/v1/')
    ? routes.find(([pattern]) => pattern.test(path))
    : undefined
  if (!route)
    return Response.json(
      { error: { code: 'not_found', message: 'Unknown automation API endpoint.' } },
      { status: 404, headers: privateHeaders },
    )
  if (!route[1].includes(request.method))
    return new Response(null, {
      status: 405,
      headers: { ...privateHeaders, Allow: route[1].join(', ') },
    })
  const authorization = request.headers.get('Authorization')
  if (!authorization?.startsWith('Bearer hp_'))
    return Response.json(
      { error: { code: 'unauthorized', message: 'A machine bearer API key is required.' } },
      { status: 401, headers: privateHeaders },
    )
  const editionError = editionRequestError(request, path)
  if (editionError) return editionError
  try {
    const headers = new Headers({ Authorization: authorization, Accept: 'application/json' })
    for (const name of ['Content-Type', 'Idempotency-Key']) {
      const value = request.headers.get(name)
      if (value !== null) headers.set(name, value)
    }
    const body = request.method === 'POST' ? await boundedBytes(request, 1024 * 1024) : undefined
    if (body === null)
      return Response.json(
        { error: { code: 'body_limit', message: 'Request exceeds 1 MiB.' } },
        { status: 413, headers: privateHeaders },
      )
    const response = await fetch(apiURL(path) + url.search, {
      method: request.method,
      headers,
      body,
      redirect: 'error',
      signal: AbortSignal.any([request.signal, AbortSignal.timeout(35000)]),
    })
    const returned = new Headers(privateHeaders)
    for (const name of ['Content-Type', 'Retry-After', 'WWW-Authenticate', 'Allow']) {
      const value = response.headers.get(name)
      if (value !== null) returned.set(name, value)
    }
    return new Response(response.body, { status: response.status, headers: returned })
  } catch {
    return Response.json(
      {
        error: {
          code: 'api_unavailable',
          message: 'The automation API is temporarily unavailable.',
        },
      },
      { status: 503, headers: privateHeaders },
    )
  }
}
