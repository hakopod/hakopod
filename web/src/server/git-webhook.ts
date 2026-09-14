import { apiURL, boundedBytes, privateHeaders } from './session.ts'

const endpoint = /^\/api\/v1\/webhooks\/(git\/[A-Za-z0-9_-]+|github-app\/[1-9][0-9]*)$/
// Go verifies the provider signature. Public callbacks never inherit browser authority.
export async function forwardNamedGitWebhook(request: Request) {
  const match = new URL(request.url).pathname.match(endpoint)
  if (!match) return new Response(null, { status: 404, headers: privateHeaders })
  if (request.method !== 'POST')
    return new Response(null, { status: 405, headers: { ...privateHeaders, Allow: 'POST' } })
  try {
    const body = await boundedBytes(request, 512 * 1024)
    if (body === null)
      return Response.json(
        { error: { code: 'body_limit', message: 'Webhook body exceeds 512 KiB.' } },
        { status: 413, headers: privateHeaders },
      )
    const headers = new Headers({
      'Content-Type': request.headers.get('Content-Type') || 'application/json',
      Accept: 'application/json',
    })
    for (const name of [
      'X-Hub-Signature-256',
      'X-GitHub-Event',
      'X-GitHub-Delivery',
      'X-Gitlab-Token',
      'X-Gitlab-Event',
      'X-Gitlab-Event-UUID',
      'X-Gitlab-Webhook-UUID',
    ]) {
      const value = request.headers.get(name)
      if (value) headers.set(name, value)
    }
    const response = await fetch(apiURL(`webhooks/${match[1]}`), {
      method: 'POST',
      headers,
      body,
      redirect: 'error',
      signal: AbortSignal.any([request.signal, AbortSignal.timeout(15000)]),
    })
    return new Response(response.body, {
      status: response.status,
      headers: { ...privateHeaders, 'Content-Type': 'application/json' },
    })
  } catch {
    return Response.json(
      {
        error: {
          code: 'webhook_unavailable',
          message: 'The repository webhook endpoint is temporarily unavailable.',
        },
      },
      { status: 503, headers: privateHeaders },
    )
  }
}
