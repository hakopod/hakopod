import { createFileRoute } from '@tanstack/react-router'
import { authenticatedResponse, oauth } from '../server/auth'
import { forwardGitHubWebhook } from '../server/github-webhook'
import { forwardGitLabWebhook } from '../server/gitlab-webhook'
import {
  apiURL,
  boundedBody,
  privateHeaders,
  requireSameOrigin,
  sessionToken,
} from '../server/session'

const allowed = [
  /^teams\/[A-Za-z0-9_-]+$/,
  /^license$/,
  /^applications\/[A-Za-z0-9_-]+\/logs\/query$/,
  /^applications\/[A-Za-z0-9_-]+\/services\/[A-Za-z0-9_-]+\/terminal(?:\/[A-Za-z0-9_-]+(?:\/(?:output|input))?)?$/,
  /^(me|projects|applications(?:\/[A-Za-z0-9_-]+(?:\/(?:logs|rollback)|\/services\/[A-Za-z0-9_-]+\/(?:runtime|restart|scale|tls))?)?|plan|deployments(?:\/[A-Za-z0-9_-]+(?:\/cancel)?)?|nodes|keys(?:\/[A-Za-z0-9_-]+(?:\/rotate)?)?|audit|settings\/appearance)$/,
  /^auth\/(?:status|security|sessions(?:\/[A-Za-z0-9_-]+)?|device(?:\/approve)?|mfa\/totp\/(?:start|confirm|disable)|passkeys\/(?:(?:register|login)\/(?:start|finish)|[A-Za-z0-9_-]+))$/,
  /^teams(?:\/[A-Za-z0-9_-]+\/(?:members(?:\/[A-Za-z0-9_-]+)?|invites))?$/,
  /^users(?:\/[A-Za-z0-9_-]+)?$/,
  /^projects\/[A-Za-z0-9_-]+\/(?:members|invites)$/,
  /^registries(?:\/[A-Za-z0-9_-]+(?:\/sync)?)?$/,
  /^tls\/issuers$/,
  /^settings\/haproxy$/,
  /^nodes\/(?:[A-Za-z0-9_.-]+\/(?:cordon|drain)|enrollments(?:\/[A-Za-z0-9_-]+)?)$/,
  /^builds(?:\/[A-Za-z0-9_-]+(?:\/(?:preview|install|run|runs(?:\/[A-Za-z0-9_-]+(?:\/(?:plan|deploy|cancel))?)?))?)?$/,
  /^integrations\/(?:github|gitlab)$/,
  /^applications\/[A-Za-z0-9_-]+\/source(?:\/(?:plan|deploy))?$/,
  /^templates(?:\/[A-Za-z0-9_-]+\/plan)?$/,
  /^secrets(?:\/[A-Za-z0-9_-]+)?$/,
]

async function proxy({ request, params }: { request: Request; params: { _splat?: string } }) {
  try {
    if (params._splat === 'v1/webhooks/github') return forwardGitHubWebhook(request)
    if (params._splat === 'v1/webhooks/gitlab') return forwardGitLabWebhook(request)
    if (request.method !== 'GET') {
      const rejected = requireSameOrigin(request)
      if (rejected) return rejected
    }
    const path = (params._splat || '').replace(/^v1\/(auth\/oauth\/)/, '$1')
    if (/^auth\/oauth\/(github|google|gitlab)\/(start|callback)$/.test(path))
      return oauth(request, path)
    if (!allowed.some((pattern) => pattern.test(path)))
      return Response.json({ error: { message: 'Unknown API endpoint.' } }, { status: 404 })
    const token = sessionToken(request)
    const publicPath =
      (path === 'auth/status' && request.method === 'GET') ||
      (/^auth\/passkeys\/login\/(start|finish)$/.test(path) && request.method === 'POST')
    if (!token && !publicPath)
      return Response.json(
        { error: { code: 'unauthorized', message: 'Sign in to your account to continue.' } },
        { status: 401, headers: privateHeaders },
      )
    const headers = new Headers({ Accept: 'application/json' })
    if (token) headers.set('Authorization', `Bearer ${token}`)
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
    const terminalStream = /\/terminal\/[A-Za-z0-9_-]+\/output$/.test(path)
    const streaming = path.endsWith('/logs') || terminalStream
    const response = await fetch(apiURL(path) + new URL(request.url).search, {
      method: request.method,
      headers,
      body,
      redirect: 'error',
      signal: AbortSignal.any([
        request.signal,
        AbortSignal.timeout(terminalStream ? 11 * 60 * 1000 : streaming ? 5 * 60 * 1000 : 30000),
      ]),
    })
    if (path === 'auth/passkeys/login/finish') return authenticatedResponse(request, response)
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
  server: { handlers: { GET: proxy, POST: proxy, DELETE: proxy, PATCH: proxy, PUT: proxy } },
})
