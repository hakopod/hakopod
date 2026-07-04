import { apiURL, boundedBytes, privateHeaders } from './session.ts'

// This exact public route uses GitLab shared-secret authentication in Go. It never
// inherits browser cookies, bearer credentials, or browser-origin privileges.
export async function forwardGitLabWebhook(request: Request) {
  if (new URL(request.url).pathname !== '/api/v1/webhooks/gitlab')
    return new Response(null, { status: 404, headers: privateHeaders })
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
      'X-Gitlab-Token',
      'X-Gitlab-Event',
      'X-Gitlab-Event-UUID',
      'X-Gitlab-Webhook-UUID',
    ]) {
      const value = request.headers.get(name)
      if (value) headers.set(name, value)
    }
    const response = await fetch(apiURL('webhooks/gitlab'), {
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
          message: 'The GitLab webhook endpoint is temporarily unavailable.',
        },
      },
      { status: 503, headers: privateHeaders },
    )
  }
}
