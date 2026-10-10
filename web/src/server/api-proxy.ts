import { forwardAutomationAPI } from './automation-api.ts'
import { forwardMCP } from './mcp.ts'
import { editionHeaders, editionRequestError, editionResponseHeaders } from './dashboard-edition.ts'
import { authenticatedResponse, oauth } from './auth.ts'
import { forwardNamedGitWebhook } from './git-webhook.ts'
import { forwardGitHubWebhook } from './github-webhook.ts'
import { forwardGitLabWebhook } from './gitlab-webhook.ts'
import {
  apiURL,
  boundedBody,
  callbackSessionCookie,
  callbackSessionToken,
  privateHeaders,
  requireSameOrigin,
  sessionToken,
} from './session.ts'

export const allowed = [
  /^database-explorer\/connections$/,
  /^managed-platforms(?:\/(?:catalog|reviews|operations|[a-f0-9]{32}(?:\/(?:operations|recovery-operations|trust))?))?$/,
  /^managed-platform-operations\/[a-f0-9]{32}$/,
  /^managed-platform-recovery\/(?:reviews|operations)$/,
  /^managed-platform-recovery-operations\/[a-f0-9]{32}(?:\/cancel)?$/,
  /^external-databases(?:\/[a-f0-9]{32}(?:\/(?:connections|trust|connection-plan|connect))?)?$/,
  /^external-database-operations\/[a-f0-9]{32}$/,
  /^databases(?:\/[a-f0-9]{32}(?:\/(?:operations|connections|trust|metrics|failures|query|query-capabilities|credentials|resize-plan|resize-retry-plan|resize-retry|resize|restore-plan|connection-plan|connect|inspect|switchover-plan|switchover|switchover-retry|application-provisioning-plan|application-provision|migration-lock-recovery-plan|migration-lock-recover|public-endpoint-capabilities|public-endpoint-plan|public-endpoints(?:\/[a-f0-9]{32})?))?)?$/,
  /^database-operations\/[a-f0-9]{32}$/,
  /^database-(?:application-provisioning|migration-lock-recovery)-operations\/[a-f0-9]{32}$/,
  /^databases\/[a-f0-9]{32}\/private-access$/,
  /^database-capacity-plan$/,
  /^database-public-endpoint-operations\/[a-f0-9]{32}$/,
  /^actions\/capabilities$/,
  /^applications\/[A-Za-z0-9_-]+\/actions$/,
  /^applications\/[A-Za-z0-9_-]+\/actions\/[A-Za-z0-9_-]+\/jobs(?:\/[A-Za-z0-9_-]+\/(?:logs|cancel))?$/,
  /^applications\/[A-Za-z0-9_-]+\/actions\/[A-Za-z0-9_-]+\/hold(?:\/release)?$/,
  /^storage\/retained(?:\/[a-f0-9]{32})?$/,
  /^roles(?:\/[A-Za-z0-9_:-]+)?$/,
  /^organization\/security$/,
  /^auth\/mfa\/verify$/,
  /^applications\/[A-Za-z0-9_-]+\/volume-resizes(?:\/plan|\/[A-Za-z0-9_-]+\/(?:retry|cancel|retain|delete-original))?$/,
  /^deployments\/[A-Za-z0-9_-]+\/volume-cleanup$/,
  /^placement\/nodes$/,
  /^database-placement\/nodes$/,
  /^requests$/,
  /^applications\/[A-Za-z0-9_-]+\/services\/[A-Za-z0-9_-]+\/requests\/routing$/,
  /^applications\/[A-Za-z0-9_-]+\/services\/[A-Za-z0-9_-]+\/bindings\/[A-Za-z_][A-Za-z0-9_]{0,127}(?:\/test)?$/,
  /^applications\/[A-Za-z0-9_-]+\/notifications(?:\/[A-Za-z0-9_-]+(?:\/test)?)?$/,
  /^applications\/[A-Za-z0-9_-]+\/provenance$/,
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
  /^installation\/cleanup\/(?:review|execute|operations\/[a-f0-9]{32})$/,
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
  /^nodes\/[A-Za-z0-9_.-]+\/terminal(?:\/[A-Za-z0-9_-]+(?:\/(?:output|input|poll))?)?$/,
  /^backup-destinations(?:\/[A-Za-z0-9_-]+(?:\/test)?)?$/,
  /^backup-targets$/,
  /^backup-imports(?:\/[a-f0-9]{32}(?:\/archive)?)?$/,
  /^backups(?:\/[A-Za-z0-9_-]+(?:\/cancel)?)?$/,
  /^backup-artifacts(?:\/[A-Za-z0-9_-]+(?:\/(?:restore-plan|restore))?)?$/,
  /^backup-schedules(?:\/[A-Za-z0-9_-]+)?$/,
  /^teams\/[A-Za-z0-9_-]+$/,
  /^license$/,
  /^applications\/[A-Za-z0-9_-]+\/logs\/query$/,
  /^applications\/[A-Za-z0-9_-]+\/services\/[A-Za-z0-9_-]+\/terminal(?:\/[A-Za-z0-9_-]+(?:\/(?:output|input|poll))?)?$/,
  /^(me|projects(?:\/[A-Za-z0-9_-]+)?|applications(?:\/[A-Za-z0-9_-]+(?:\/(?:logs|rollback)|\/services\/[A-Za-z0-9_-]+\/(?:runtime|restart|stop|resume|scale|tls|delivery|certificates|exec))?)?|plan|deployments(?:\/[A-Za-z0-9_-]+(?:\/(?:cancel|resume))?)?|nodes|keys(?:\/[A-Za-z0-9_-]+(?:\/rotate)?)?|audit|settings\/appearance)$/,
  /^auth\/(?:status|onboarding|invites\/inspect|security|sessions(?:\/[A-Za-z0-9_-]+)?|device(?:\/approve)?|mfa\/totp\/(?:start|confirm|disable)|passkeys\/(?:(?:register|login)\/(?:start|finish)|[A-Za-z0-9_-]+))$/,
  /^teams(?:\/[A-Za-z0-9_-]+\/(?:members(?:\/[A-Za-z0-9_-]+)?|invites))?$/,
  /^users(?:\/[A-Za-z0-9_-]+)?$/,
  /^projects\/[A-Za-z0-9_-]+\/(?:members|invites|environments)$/,
  /^projects\/[A-Za-z0-9_-]+\/environments\/[A-Za-z0-9_-]+$/,
  /^registries(?:\/[A-Za-z0-9_-]+(?:\/sync)?)?$/,
  /^tls\/issuers$/,
  /^applications\/[A-Za-z0-9_-]+\/tls\/issuers$/,
  /^settings\/haproxy$/,
  /^nodes\/(?:[A-Za-z0-9_.-]+\/(?:cordon|drain)|enrollments(?:\/[A-Za-z0-9_-]+)?)$/,
  /^builds(?:\/[A-Za-z0-9_-]+(?:\/(?:preview|install|run|runs(?:\/[A-Za-z0-9_-]+(?:\/(?:plan|deploy|cancel))?)?))?)?$/,
  /^integrations\/(?:github|gitlab)$/,
  /^integrations\/slack(?:\/(?:connect|callback|channels|channel|events|deliveries|test))?$/,
  /^applications\/[A-Za-z0-9_-]+\/source(?:\/(?:plan|deploy))?$/,
  /^applications\/[A-Za-z0-9_-]+\/(?:services\/[A-Za-z0-9_-]+\/(?:move-plan|move)|service-moves(?:\/[A-Za-z0-9_-]+\/finish)?)$/,
  /^templates(?:\/[A-Za-z0-9_-]+\/(?:plan|deploy|secrets\/[A-Za-z0-9_-]+))?$/,
  /^secrets(?:\/[A-Za-z0-9_-]+)?$/,
  /^deployment-secret-requirements$/,
  /^secret-providers(?:\/[a-z][a-z0-9-]{0,39})?$/,
  /^dns-providers(?:\/[a-z][a-z0-9-]{0,39})?$/,
]

export function proxyTimeoutMilliseconds(path: string, method: string) {
  if (/^backup-imports\/[a-f0-9]{32}\/archive$/.test(path) && method === 'PUT')
    return 15 * 60 * 1000
  if (
    /\/terminal\/[A-Za-z0-9_-]+\/output$/.test(path) ||
    /^deployments\/[A-Za-z0-9_-]+\/events$/.test(path)
  )
    return 11 * 60 * 1000
  if (path.endsWith('/logs')) return 5 * 60 * 1000
  if (/^databases\/[a-f0-9]{32}\/restore-plan$/.test(path) && method === 'POST') return 90000
  if (/^databases\/[a-f0-9]{32}\/migration-lock-recovery-plan$/.test(path) && method === 'POST')
    return 45000
  return 30000
}

export async function proxy({
  request,
  params,
}: {
  request: Request
  params: { _splat?: string }
}) {
  try {
    // OAuth providers may return to the versioned public callback URL. Rewrite
    // only this GET into the browser-session proxy before `/api/v1/*` is sent
    // through the machine-token transport below.
    if (params._splat === 'v1/integrations/slack/callback' && request.method === 'GET')
      return proxy({ request, params: { _splat: 'integrations/slack/callback' } })
    if (/^v1\/auth\/(?:device\/(start|token)|logout)$/.test(params._splat || ''))
      return forwardAutomationAPI(request)
    if (params._splat === 'v1/mcp') return forwardMCP(request)
    if (/^v1\/webhooks\/(?:git|github-app|nodes)\//.test(params._splat || ''))
      return forwardNamedGitWebhook(request)
    if (params._splat === 'v1/webhooks/github') return forwardGitHubWebhook(request)
    if (params._splat === 'v1/webhooks/gitlab') return forwardGitLabWebhook(request)
    if ((params._splat || '').startsWith('v1/') && !(params._splat || '').startsWith('v1/auth/'))
      return forwardAutomationAPI(request)
    if (request.method !== 'GET') {
      const rejected = requireSameOrigin(request)
      if (rejected) return rejected
    }
    const path = (params._splat || '').replace(/^v1\/(auth\/oauth\/)/, '$1')
    if (/^auth\/oauth\/(github|google|gitlab|oidc)\/(start|callback)$/.test(path))
      return oauth(request, path)
    if (!allowed.some((pattern) => pattern.test(path)))
      return Response.json({ error: { message: 'Unknown API endpoint.' } }, { status: 404 })
    const executionPath =
      /^databases\/[a-f0-9]{32}\/query$/.test(path) ||
      /^applications\/[A-Za-z0-9_-]+\/services\/[A-Za-z0-9_-]+\/exec$/.test(path)
    if (executionPath && request.method !== 'POST')
      return Response.json(
        { error: { message: 'Execution endpoints require POST.' } },
        { status: 405, headers: { ...privateHeaders, Allow: 'POST' } },
      )
    const actionsMethod =
      /^applications\/[A-Za-z0-9_-]+\/actions\/[A-Za-z0-9_-]+\/(?:jobs\/[A-Za-z0-9_-]+\/cancel|hold\/release)$/.test(
        path,
      )
        ? 'POST'
        : /^applications\/[A-Za-z0-9_-]+\/actions\/[A-Za-z0-9_-]+\/hold$/.test(path)
          ? 'GET'
          : undefined
    if (actionsMethod && request.method !== actionsMethod)
      return Response.json(
        { error: { message: 'Unsupported method for this runner action.' } },
        { status: 405, headers: { ...privateHeaders, Allow: actionsMethod } },
      )
    const slackCallback = path === 'integrations/slack/callback' && request.method === 'GET'
    const token =
      sessionToken(request) ||
      (slackCallback ? callbackSessionToken(request, 'hakopod_slack_callback') : null)
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
    const archiveUpload =
      /^backup-imports\/[a-f0-9]{32}\/archive$/.test(path) && request.method === 'PUT'
    let body: BodyInit | undefined
    if (archiveUpload) {
      const limit = 64 * 1024 * 1024
      const length = request.headers.get('content-length')
      if (request.headers.get('content-type') !== 'application/octet-stream' || !request.body)
        return Response.json(
          { error: { message: 'Upload a raw database archive.' } },
          { status: 400, headers: privateHeaders },
        )
      if (length !== null && (!/^\d+$/.test(length) || Number(length) > limit))
        return Response.json(
          {
            error: {
              message: 'Browser imports are limited to 64 MiB. Use the CLI for larger archives.',
            },
          },
          { status: 413, headers: privateHeaders },
        )
      let received = 0
      body = request.body.pipeThrough(
        new TransformStream<Uint8Array, Uint8Array>({
          transform(chunk, controller) {
            received += chunk.byteLength
            if (received > limit) throw new Error('Archive exceeds browser upload limit')
            controller.enqueue(chunk)
          },
        }),
      )
      headers.set('Content-Type', 'application/octet-stream')
      if (length !== null) headers.set('Content-Length', length)
    } else if (!['GET', 'HEAD'].includes(request.method)) {
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
      ...(archiveUpload ? { duplex: 'half' as const } : {}),
      redirect: slackCallback ? 'manual' : 'error',
      signal: AbortSignal.any([
        request.signal,
        AbortSignal.timeout(proxyTimeoutMilliseconds(path, request.method)),
      ]),
    })
    if (
      path === 'auth/passkeys/login/finish' ||
      (path === 'auth/onboarding' && request.method === 'POST')
    )
      return authenticatedResponse(request, response)
    if (slackCallback) {
      const location = response.headers.get('Location')
      if (
        response.status === 303 &&
        location &&
        /^\/settings\/integrations\/slack(?:\?.*)?$/.test(location)
      ) {
        const headers = new Headers({ ...privateHeaders, Location: location })
        headers.append(
          'Set-Cookie',
          callbackSessionCookie(request, 'hakopod_slack_callback', '', true),
        )
        return new Response(null, { status: 303, headers })
      }
      const headers = new Headers({ ...privateHeaders })
      headers.append(
        'Set-Cookie',
        callbackSessionCookie(request, 'hakopod_slack_callback', '', true),
      )
      return new Response(response.body, {
        status: response.status,
        headers: {
          ...Object.fromEntries(headers),
          'Content-Type': response.headers.get('content-type') || 'application/json',
        },
      })
    }
    const callbackCookie =
      path === 'integrations/slack/connect' && request.method === 'POST' && response.ok && token
        ? callbackSessionCookie(request, 'hakopod_slack_callback', token)
        : undefined
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
        ...(callbackCookie ? { 'Set-Cookie': callbackCookie } : {}),
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
