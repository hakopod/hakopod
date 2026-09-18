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
  const authorization = request.headers.get('Authorization')
  if (!authorization?.startsWith('Bearer '))
    return Response.json(
      { error: { code: 'unauthorized', message: 'A scoped bearer API key is required.' } },
      { status: 401, headers: privateHeaders },
    )
  try {
    const headers = new Headers({ Authorization: authorization })
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
    const body = request.method === 'POST' ? await boundedBytes(request, 512 * 1024) : undefined
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
