import { apiURL, boundedBytes, privateHeaders } from './session.ts'

// External MCP clients supply their own scoped key. Never substitute a browser
// session or forward its cookies, workspace headers or arbitrary upstream headers.
export async function forwardMCP(request: Request) {
  if (new URL(request.url).pathname !== '/api/v1/mcp')
    return new Response(null, { status: 404, headers: privateHeaders })
  if (!['GET', 'POST', 'DELETE'].includes(request.method))
    return new Response(null, {
      status: 405,
      headers: { ...privateHeaders, Allow: 'POST, DELETE' },
    })
  // Without a bearer credential the API answers 401 with the OAuth discovery
  // challenge for this connection, so only the method and query go upstream.
  const authorization = request.headers.get('Authorization')
  const bearer = authorization?.startsWith('Bearer ') ? authorization : null
  try {
    const headers = new Headers(bearer ? { Authorization: bearer } : {})
    for (const name of [
      'Content-Type',
      'Accept',
      'Origin',
      'Mcp-Session-Id',
      'MCP-Protocol-Version',
    ]) {
      const value = request.headers.get(name)
      if (value !== null) headers.set(name, value)
    }
    const body =
      bearer && request.method === 'POST' ? await boundedBytes(request, 512 * 1024) : undefined
    if (body === null)
      return Response.json(
        { error: { code: 'body_limit', message: 'MCP request exceeds 512 KiB.' } },
        { status: 413, headers: privateHeaders },
      )
    const response = await fetch(apiURL('mcp') + new URL(request.url).search, {
      method: request.method,
      headers,
      body,
      redirect: 'error',
      signal: AbortSignal.any([request.signal, AbortSignal.timeout(35000)]),
    })
    const returned = new Headers(privateHeaders)
    for (const name of [
      'Content-Type',
      'Mcp-Session-Id',
      'MCP-Protocol-Version',
      'Allow',
      'Retry-After',
      'WWW-Authenticate',
    ]) {
      const value = response.headers.get(name)
      if (value !== null) returned.set(name, value)
    }
    return new Response(response.body, { status: response.status, headers: returned })
  } catch {
    return Response.json(
      {
        error: { code: 'mcp_unavailable', message: 'The MCP endpoint is temporarily unavailable.' },
      },
      { status: 503, headers: privateHeaders },
    )
  }
}

// OAuth discovery for MCP clients lives at the site root; the API answers it.
export async function forwardOAuthMetadata(request: Request) {
  const url = new URL(request.url)
  if (
    request.method !== 'GET' ||
    !/^\/\.well-known\/(?:oauth-authorization-server|oauth-protected-resource(?:\/api\/v1\/mcp)?)$/.test(
      url.pathname,
    )
  )
    return new Response(null, { status: 404, headers: privateHeaders })
  try {
    const response = await fetch(new URL(apiURL('')).origin + url.pathname + url.search, {
      headers: { Accept: 'application/json' },
      redirect: 'error',
      signal: AbortSignal.any([request.signal, AbortSignal.timeout(10000)]),
    })
    const returned = new Headers({ 'Access-Control-Allow-Origin': '*' })
    for (const name of ['Content-Type', 'Cache-Control']) {
      const value = response.headers.get(name)
      if (value !== null) returned.set(name, value)
    }
    return new Response(response.body, { status: response.status, headers: returned })
  } catch {
    return new Response(null, { status: 503, headers: privateHeaders })
  }
}
