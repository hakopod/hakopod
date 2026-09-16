import { editionHeaders, editionRequestError, editionResponseHeaders } from './dashboard-edition.ts'
import { authenticatedResponse, oauth } from './auth.ts'
import { forwardNamedGitWebhook } from './git-webhook.ts'
import { forwardGitHubWebhook } from './github-webhook.ts'
import { forwardGitLabWebhook } from './gitlab-webhook.ts'
import { apiURL, boundedBody, privateHeaders, requireSameOrigin, sessionToken } from './session.ts'

export const allowed = [
  /^idempotency\/[A-Za-z0-9_.:-]+$/,
  /^deployments\/[A-Za-z0-9_-]+\/events$/,
  /^cloud\/capabilities$/,
  /^openapi\.json$/,
  /^compose\/convert$/,
  /^(?:projects\/[A-Za-z0-9_-]+|applications\/[A-Za-z0-9_-]+(?:\/services\/[A-Za-z0-9_-]+)?)\/name$/,
  /^applications\/[A-Za-z0-9_-]+\/previews$/,
  /^previews\/[A-Za-z0-9_-]+$/,
  /^builds\/detect$/,
  /^installation\/(?:status|logs(?:\/query)?|setup|upgrade)$/,
  /^git\/connections(?:\/[A-Za-z0-9_-]+(?:\/(?:authorize|oauth\/complete|github\/setup|repositories))?)?$/,
  /^git\/setup$/,
  /^git\/oauth\/complete$/,
  /^git\/github\/(?:start|complete|install\/complete)$/,
  /^installation\/smtp(?:\/test)?$/,
  /^installation\/login-providers\/(?:github|google|gitlab|oidc)$/,
  /^audit\/(?:history|export)$/,
  /^alarms(?:\/[A-Za-z0-9_-]+\/(?:read|acknowledge))?$/,
  /^alarm-settings$/,
  /^virtual-networks(?:\/[A-Za-z0-9_-]+(?:\/candidates)?)?$/,
  /^sources\/(?:plan|deploy)$/,
  /^showcase(?:\/remove)?$/,
  /^applications\/[A-Za-z0-9_-]+\/domains(?:\/[A-Za-z0-9_.-]+(?:\/verify)?)?$/,
  /^auth\/profile$/,
  /^teams\/[A-Za-z0-9_-]+\/members\/[A-Za-z0-9_-]+\/username$/,
  /^host-access(?:\/[A-Za-z0-9_-]+(?:\/(?:[A-Za-z0-9_.-]+|\*))?)?$/,
  /^nodes\/[A-Za-z0-9_.-]+\/terminal(?:\/[A-Za-z0-9_-]+(?:\/(?:output|input))?)?$/,
  /^backup-destinations(?:\/[A-Za-z0-9_-]+(?:\/test)?)?$/,
  /^backup-targets$/,
  /^backups(?:\/[A-Za-z0-9_-]+(?:\/cancel)?)?$/,
  /^backup-artifacts(?:\/[A-Za-z0-9_-]+(?:\/(?:restore-plan|restore))?)?$/,
  /^backup-schedules(?:\/[A-Za-z0-9_-]+)?$/,
  /^teams\/[A-Za-z0-9_-]+$/,
  /^license$/,
  /^applications\/[A-Za-z0-9_-]+\/logs\/query$/,
  /^applications\/[A-Za-z0-9_-]+\/services\/[A-Za-z0-9_-]+\/terminal(?:\/[A-Za-z0-9_-]+(?:\/(?:output|input))?)?$/,
  /^(me|projects(?:\/[A-Za-z0-9_-]+)?|applications(?:\/[A-Za-z0-9_-]+(?:\/(?:logs|rollback)|\/services\/[A-Za-z0-9_-]+\/(?:runtime|restart|stop|resume|scale|tls|delivery|certificates))?)?|plan|deployments(?:\/[A-Za-z0-9_-]+(?:\/cancel)?)?|nodes|keys(?:\/[A-Za-z0-9_-]+(?:\/rotate)?)?|audit|settings\/appearance)$/,
  /^auth\/(?:status|onboarding|invites\/inspect|security|sessions(?:\/[A-Za-z0-9_-]+)?|device(?:\/approve)?|mfa\/totp\/(?:start|confirm|disable)|passkeys\/(?:(?:register|login)\/(?:start|finish)|[A-Za-z0-9_-]+))$/,
  /^teams(?:\/[A-Za-z0-9_-]+\/(?:members(?:\/[A-Za-z0-9_-]+)?|invites))?$/,
  /^users(?:\/[A-Za-z0-9_-]+)?$/,
  /^projects\/[A-Za-z0-9_-]+\/(?:members|invites|environments)$/,
  /^registries(?:\/[A-Za-z0-9_-]+(?:\/sync)?)?$/,
  /^tls\/issuers$/,
  /^settings\/haproxy$/,
  /^nodes\/(?:[A-Za-z0-9_.-]+\/(?:cordon|drain)|enrollments(?:\/[A-Za-z0-9_-]+)?)$/,
  /^builds(?:\/[A-Za-z0-9_-]+(?:\/(?:preview|install|run|runs(?:\/[A-Za-z0-9_-]+(?:\/(?:plan|deploy|cancel))?)?))?)?$/,
  /^integrations\/(?:github|gitlab)$/,
  /^applications\/[A-Za-z0-9_-]+\/source(?:\/(?:plan|deploy))?$/,
  /^applications\/[A-Za-z0-9_-]+\/(?:services\/[A-Za-z0-9_-]+\/(?:move-plan|move)|service-moves(?:\/[A-Za-z0-9_-]+\/finish)?)$/,
  /^templates(?:\/[A-Za-z0-9_-]+\/(?:plan|deploy|secrets\/[A-Za-z0-9_-]+))?$/,
  /^secrets(?:\/[A-Za-z0-9_-]+)?$/,
  /^secret-providers(?:\/[a-z][a-z0-9-]{0,39})?$/,
]

export async function proxy({
  request,
  params,
}: {
  request: Request
  params: { _splat?: string }
}) {
  try {
    if (/^v1\/webhooks\/(?:git|github-app|nodes)\//.test(params._splat || ''))
      return forwardNamedGitWebhook(request)
    if (params._splat === 'v1/webhooks/github') return forwardGitHubWebhook(request)
    if (params._splat === 'v1/webhooks/gitlab') return forwardGitLabWebhook(request)
    if (request.method !== 'GET') {
      const rejected = requireSameOrigin(request)
      if (rejected) return rejected
    }
    const path = (params._splat || '').replace(/^v1\/(auth\/oauth\/)/, '$1')
    if (/^auth\/oauth\/(github|google|gitlab|oidc)\/(start|callback)$/.test(path))
      return oauth(request, path)
    if (!allowed.some((pattern) => pattern.test(path)))
      return Response.json({ error: { message: 'Unknown API endpoint.' } }, { status: 404 })
    const token = sessionToken(request)
    const publicPath =
      (path === 'auth/status' && request.method === 'GET') ||
      (path === 'auth/invites/inspect' && request.method === 'POST') ||
      (/^auth\/passkeys\/login\/(start|finish)$/.test(path) && request.method === 'POST')
    if (!token && !publicPath)
      return Response.json(
        { error: { code: 'unauthorized', message: 'Sign in to your account to continue.' } },
        { status: 401, headers: privateHeaders },
      )
    const editionError = editionRequestError(request, path)
    if (editionError) return editionError
    const headers = new Headers({ Accept: 'application/json', ...editionHeaders(request) })
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
    const deploymentEvents = /^deployments\/[A-Za-z0-9_-]+\/events$/.test(path)
    if (deploymentEvents) {
      const cursor = request.headers.get('Last-Event-ID')
      if (cursor && /^\d{1,19}$/.test(cursor)) headers.set('Last-Event-ID', cursor)
    }
    const streaming = path.endsWith('/logs') || terminalStream || deploymentEvents
    const response = await fetch(apiURL(path) + new URL(request.url).search, {
      method: request.method,
      headers,
      body,
      redirect: 'error',
      signal: AbortSignal.any([
        request.signal,
        AbortSignal.timeout(
          terminalStream || deploymentEvents ? 11 * 60 * 1000 : streaming ? 5 * 60 * 1000 : 30000,
        ),
      ]),
    })
    if (
      path === 'auth/passkeys/login/finish' ||
      (path === 'auth/onboarding' && request.method === 'POST')
    )
      return authenticatedResponse(request, response)
    return new Response(response.body, {
      status: response.status,
      headers: {
        ...privateHeaders,
        ...editionResponseHeaders(request),
        'Content-Type': response.headers.get('content-type') || 'application/json',
        ...(streaming ? { 'X-Accel-Buffering': 'no' } : {}),
        ...(path === 'audit/export' && response.headers.has('X-Hakopod-Next-Cursor')
          ? { 'X-Hakopod-Next-Cursor': response.headers.get('X-Hakopod-Next-Cursor')! }
          : {}),
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
