import { apiURL, boundedBody, privateHeaders, requireSameOrigin, sessionToken } from './session.ts'
import { explorerEditionHeaders } from './dashboard-edition.ts'

type ExplorerScope = { project: string; environment: string; workspace: string }
export function explorerScope(value: string): ExplorerScope {
  if (!/^[A-Za-z0-9_-]{1,1024}$/.test(value)) throw new Error('Invalid explorer scope.')
  const parsed = JSON.parse(Buffer.from(value, 'base64url').toString('utf8'))
  if (
    Buffer.from(JSON.stringify(parsed)).toString('base64url') !== value ||
    Object.keys(parsed).sort().join(',') !== 'environment,project,workspace' ||
    !/^[a-z][a-z0-9-]{0,62}$/.test(parsed.project) ||
    !/^[a-z][a-z0-9-]{0,62}$/.test(parsed.environment) ||
    (parsed.workspace !== '' && !/^[a-f0-9]{32}$/.test(parsed.workspace))
  )
    throw new Error('Invalid explorer scope.')
  return parsed
}
function unavailable(request: Request, message: string, status: number) {
  if (/\/api\//.test(new URL(request.url).pathname))
    return Response.json({ error: message }, { status, headers: privateHeaders })
  return new Response(
    `<!doctype html><html lang="en"><title>Database explorer</title><main><h1>Database explorer</h1><p>${message}</p><a href="/">Back to Hakopod</a></main></html>`,
    { status, headers: { ...privateHeaders, 'Content-Type': 'text/html; charset=utf-8' } },
  )
}
export async function proxyExplorer({ request }: { request: Request }) {
  try {
    const url = new URL(request.url)
    const match = /^\/synehq\/s\/([A-Za-z0-9_-]{1,1024})(\/.*)?$/.exec(url.pathname)
    if (!match || url.search) return unavailable(request, 'Invalid explorer path.', 400)
    const scope = explorerScope(match[1])
    const token = sessionToken(request)
    if (!token) return unavailable(request, 'Sign in to Hakopod, then reopen the explorer.', 401)
    const originError = ['GET', 'HEAD'].includes(request.method) ? null : requireSameOrigin(request)
    if (originError) return originError
    const body = ['GET', 'HEAD'].includes(request.method)
      ? ''
      : await boundedBody(request, 192 * 1024)
    if (body === null) return new Response('Request too large.', { status: 413 })
    const response = await fetch(apiURL('database-explorer/http'), {
      method: 'POST',
      headers: {
        ...explorerEditionHeaders(request, scope.workspace),
        Authorization: `Bearer ${token}`,
        'Content-Type': 'application/json',
      },
      body: JSON.stringify({
        project: scope.project,
        environment: scope.environment,
        method: request.method,
        path: match[2] || '/',
        body,
      }),
      redirect: 'error',
      signal: AbortSignal.any([request.signal, AbortSignal.timeout(35000)]),
    })
    if (!response.ok)
      return unavailable(
        request,
        'The explorer is unavailable or access was revoked. Reopen it from Databases.',
        response.status,
      )
    // Bound the JSON envelope before decoding base64 assets or query results.
    const reader = response.body!.getReader()
    let length = 0
    const chunks: Uint8Array[] = []
    while (true) {
      const { value, done } = await reader.read()
      if (done) break
      length += value.length
      if (length > 17 * 1024 * 1024) {
        await reader.cancel()
        throw new Error('Explorer response is too large.')
      }
      chunks.push(value)
    }
    const result = JSON.parse(Buffer.concat(chunks).toString('utf8'))
    if (
      !Number.isInteger(result.status) ||
      result.status < 200 ||
      result.status > 599 ||
      typeof result.contentType !== 'string' ||
      typeof result.body !== 'string'
    )
      throw new Error('Invalid explorer response.')
    let content = Buffer.from(result.body, 'base64')
    const prefix = `/synehq/s/${match[1]}`
    if (result.contentType.startsWith('text/html'))
      content = Buffer.from(content.toString('utf8').replaceAll('/synehq/', `${prefix}/`))
    if (/javascript/.test(result.contentType))
      content = Buffer.from(
        content.toString('utf8').replaceAll('/synehq/_next/', `${prefix}/_next/`),
      )
    return new Response(request.method === 'HEAD' || result.status === 204 ? null : content, {
      status: result.status,
      headers: {
        ...privateHeaders,
        'Content-Type': result.contentType,
        'X-Content-Type-Options': 'nosniff',
        'X-Frame-Options': 'DENY',
        'Referrer-Policy': 'same-origin',
      },
    })
  } catch {
    return unavailable(request, 'The explorer is unavailable. Reopen it from Databases.', 503)
  }
}
