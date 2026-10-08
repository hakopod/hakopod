import { generatedAgentRoute } from './agent-routes.ts'
import { apiURL, boundedBytes, privateHeaders } from './session.ts'

// Public machine API: keep browser cookie authentication on /api/* separate.
// Canonical Go handlers enforce key expiry, scope, permissions and rate limits.
const routes: [RegExp, string[]][] = [
  [/^me$/, ['GET']],
  [/^auth\/logout$/, ['POST']],
  [/^projects$/, ['GET', 'POST']],
  [/^projects\/[A-Za-z0-9_-]+$/, ['DELETE']],
  [/^projects\/[A-Za-z0-9_-]+\/environments$/, ['POST']],
  [/^cloud\/capabilities$/, ['GET']],
  [/^git\/connections$/, ['GET']],
  [/^builds$/, ['GET', 'POST']],
  [/^builds\/detect$/, ['POST']],
  [/^builds\/[A-Za-z0-9_-]+$/, ['GET', 'PUT']],
  [/^builds\/[A-Za-z0-9_-]+\/(?:preview|install|run)$/, ['POST']],
  [/^builds\/[A-Za-z0-9_-]+\/runs(?:\/[A-Za-z0-9_-]+)?$/, ['GET']],
  [/^builds\/[A-Za-z0-9_-]+\/runs\/[A-Za-z0-9_-]+\/(?:plan|deploy)$/, ['POST']],
  [/^auth\/device\/(?:start|token)$/, ['POST']],
  // Session calls retain explicit owner and generation authority.
  [/^applications\/[A-Za-z0-9_-]+\/services\/[A-Za-z0-9_-]+\/sessions$/, ['GET', 'POST']],
  [/^applications\/[A-Za-z0-9_-]+\/services\/[A-Za-z0-9_-]+\/sessions\/[a-f0-9]{32}$/, ['GET', 'DELETE']],
  [/^applications\/[A-Za-z0-9_-]+\/services\/[A-Za-z0-9_-]+\/sessions\/[a-f0-9]{32}\/(?:heartbeat|call)$/, ['POST']],
  // Invocation callers use explicit machine credentials and tenant owner authority.
  [/^applications\/[A-Za-z0-9_-]+\/services\/[A-Za-z0-9_-]+\/invocations$/, ['GET', 'POST']],
  [/^applications\/[A-Za-z0-9_-]+\/services\/[A-Za-z0-9_-]+\/invocations\/[a-f0-9]{32}$/, ['GET']],
  [/^applications\/[A-Za-z0-9_-]+\/services\/[A-Za-z0-9_-]+\/invocations\/[a-f0-9]{32}\/cancel$/, ['POST']],
  [/^applications\/[A-Za-z0-9_-]+\/services\/[A-Za-z0-9_-]+\/invocations\/[a-f0-9]{32}\/logs$/, ['GET']],
  [/^applications$/, ['GET']],
  [/^applications\/[A-Za-z0-9_-]+$/, ['GET', 'DELETE']],
  [/^applications\/[A-Za-z0-9_-]+\/provenance$/, ['GET']],
  [/^applications\/[A-Za-z0-9_-]+\/rollback$/, ['POST']],
  [/^applications\/[A-Za-z0-9_-]+\/logs$/, ['GET']],
  [/^applications\/[A-Za-z0-9_-]+\/logs\/query$/, ['POST']],
  [/^applications\/[A-Za-z0-9_-]+\/services\/[A-Za-z0-9_-]+\/runtime$/, ['GET']],
  [/^tls\/issuers$/, ['GET']],
  [/^applications\/[A-Za-z0-9_-]+\/tls\/issuers$/, ['GET', 'POST']],
  [/^applications\/[A-Za-z0-9_-]+\/services\/[A-Za-z0-9_-]+\/tls$/, ['GET', 'POST']],
  [
    /^applications\/[A-Za-z0-9_-]+\/services\/[A-Za-z0-9_-]+\/(?:scale|restart|stop|resume)$/,
    ['POST'],
  ],
  [/^databases$/, ['GET', 'POST']],
  [/^databases\/[a-f0-9]{32}\/query$/, ['POST']],
  [/^applications\/[A-Za-z0-9_-]+\/services\/[A-Za-z0-9_-]+\/exec$/, ['POST']],
  [/^managed-platforms(?:\/catalog)?$/, ['GET']],
  [/^managed-platforms\/[a-f0-9]{32}(?:\/(?:operations|recovery-operations|trust))?$/, ['GET']],
  [/^managed-platforms\/(?:reviews|operations)$/, ['POST']],
  [/^managed-platform-operations\/[a-f0-9]{32}$/, ['GET']],
  [/^managed-platform-recovery\/(?:reviews|operations)$/, ['POST']],
  [/^managed-platform-recovery-operations\/[a-f0-9]{32}$/, ['GET']],
  [/^managed-platform-recovery-operations\/[a-f0-9]{32}\/cancel$/, ['POST']],
  [/^database-placement\/nodes$/, ['GET']],
  [/^external-databases$/, ['GET', 'POST']],
  [/^external-databases\/[a-f0-9]{32}$/, ['GET', 'PUT', 'DELETE']],
  [/^external-databases\/[a-f0-9]{32}\/(?:connections|trust)$/, ['GET']],
  [/^external-databases\/[a-f0-9]{32}\/(?:connection-plan|connect)$/, ['POST']],
  [/^external-database-operations\/[a-f0-9]{32}$/, ['GET']],
  [/^databases\/[A-Za-z0-9_-]+$/, ['GET', 'DELETE']],
  [/^databases\/[A-Za-z0-9_-]+\/(?:operations|connections|trust|metrics)$/, ['GET']],
  [
    /^databases\/[A-Za-z0-9_-]+\/(?:credentials|resize-plan|resize-retry-plan|resize-retry|resize|restore-plan|connection-plan|connect|inspect|switchover-plan|switchover|switchover-retry)$/,
    ['POST'],
  ],
  [/^database-operations\/[A-Za-z0-9_-]+$/, ['GET']],
  [/^databases\/[a-f0-9]{32}\/public-endpoint-capabilities$/, ['GET']],
  [/^databases\/[a-f0-9]{32}\/public-endpoint-plan$/, ['POST']],
  [/^databases\/[a-f0-9]{32}\/public-endpoints$/, ['GET', 'POST']],
  [/^databases\/[a-f0-9]{32}\/public-endpoints\/[a-f0-9]{32}$/, ['DELETE']],
  [/^database-public-endpoint-operations\/[a-f0-9]{32}$/, ['GET']],
  [/^backup-destinations$/, ['GET', 'POST']],
  [/^backup-destinations\/[A-Za-z0-9_-]+$/, ['PUT', 'DELETE']],
  [/^backup-destinations\/[A-Za-z0-9_-]+\/test$/, ['POST']],
  [/^backup-targets$/, ['GET']],
  [/^backups$/, ['GET', 'POST']],
  [/^backups\/[A-Za-z0-9_-]+$/, ['GET']],
  [/^backups\/[A-Za-z0-9_-]+\/cancel$/, ['POST']],
  [/^backup-artifacts$/, ['GET']],
  [/^backup-artifacts\/[A-Za-z0-9_-]+$/, ['GET', 'DELETE']],
  [/^backup-artifacts\/[A-Za-z0-9_-]+\/(?:restore-plan|restore)$/, ['POST']],
  [/^backup-schedules$/, ['GET', 'POST']],
  [/^backup-schedules\/[A-Za-z0-9_-]+$/, ['PUT', 'DELETE']],
  [/^backup-imports$/, ['POST']],
  [/^backup-imports\/[A-Za-z0-9_-]+$/, ['GET']],
  [/^virtual-networks$/, ['GET', 'POST']],
  [/^virtual-networks\/plan$/, ['POST']],
  [/^virtual-networks\/[A-Za-z0-9_-]+$/, ['GET', 'PUT', 'DELETE']],
  [/^secrets$/, ['GET']],
  [/^secrets\/[A-Za-z0-9_.-]+$/, ['POST', 'PUT', 'DELETE']],
  [/^plan$/, ['POST']],
  [/^deployments$/, ['POST']],
  [/^deployments\/[A-Za-z0-9_-]+$/, ['GET']],
  [/^deployments\/[A-Za-z0-9_-]+\/cancel$/, ['POST']],
  [/^idempotency\/[A-Za-z0-9_.:-]+$/, ['GET']],
  [/^openapi\.json$/, ['GET']],
]

export async function forwardAutomationAPI(request: Request) {
  const url = new URL(request.url)
  const path = url.pathname.slice('/api/v1/'.length)
  const route = url.pathname.startsWith('/api/v1/')
    ? routes.find(([pattern]) => pattern.test(path)) || (() => {const generated=generatedAgentRoute(path);return generated ? [new RegExp('^$'), generated.methods] as [RegExp,string[]] : undefined})()
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
  const deviceExchange = /^auth\/device\/(start|token)$/.test(path)
  if (!deviceExchange && !/^Bearer (hp_|hs_)/.test(authorization || ''))
    return Response.json(
      { error: { code: 'unauthorized', message: 'A CLI session or machine API key is required.' } },
      { status: 401, headers: privateHeaders },
    )
  try {
    const headers = new Headers({ Accept: 'application/json' })
    if (!deviceExchange && authorization) headers.set('Authorization', authorization)
    const workspace = request.headers.get('X-Hakopod-Workspace')
    if (workspace && !/^[a-f0-9]{32}$/.test(workspace))
      return Response.json(
        { error: { code: 'invalid_scope', message: 'Use an exact Cloud workspace ID.' } },
        { status: 400, headers: privateHeaders },
      )
    if (!deviceExchange && workspace && /^[a-f0-9]{32}$/.test(workspace))
      headers.set('X-Hakopod-Workspace', workspace)
    for (const name of ['Content-Type', 'Idempotency-Key']) {
      const value = request.headers.get(name)
      if (value !== null) headers.set(name, value)
    }
    if (/^applications\/[A-Za-z0-9_-]+\/services\/[A-Za-z0-9_-]+\/(?:invocations|sessions)(?:\/|$)/.test(path)) {
      const owner = request.headers.get('X-Hakopod-Owner-Scope')
      if (owner !== null) headers.set('X-Hakopod-Owner-Scope', owner)
    }
    if (/^applications\/[A-Za-z0-9_-]+\/services\/[A-Za-z0-9_-]+\/sessions(?:\/|$)/.test(path)) {
      const generation = request.headers.get('X-Hakopod-Session-Generation')
      if (generation !== null) headers.set('X-Hakopod-Session-Generation', generation)
      if (path.endsWith('/call')) return forwardSessionCall(request, path, headers)
    }
    const body = ['POST', 'PUT', 'PATCH', 'DELETE'].includes(request.method)
      ? await boundedBytes(request, 1024 * 1024)
      : undefined
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

let activeSessionCalls = 0

// Keep the slot until output is consumed or cancelled, not merely until headers arrive.
async function forwardSessionCall(request: Request, path: string, headers: Headers) {
  if (activeSessionCalls >= 2)
    return Response.json({ error: { code: 'capacity', message: 'Session transport capacity is full. No call started. Retry this request later.' } }, { status: 429, headers: privateHeaders })
  activeSessionCalls++
  let released = false
  const release = () => { if (!released) { released = true; activeSessionCalls-- } }
  try {
    const inputAbort = new AbortController()
    const inputTimer = setTimeout(() => inputAbort.abort(), 15000)
    let body: Uint8Array<ArrayBuffer> | null
    try {
      const input = request.body?.pipeThrough(new TransformStream(), { signal: AbortSignal.any([request.signal, inputAbort.signal]) }) ?? null
      body = await boundedBytes({ headers: request.headers, body: input }, 48 * 1024 * 1024)
    } finally { clearTimeout(inputTimer) }
    if (body === null) {
      release()
      return Response.json({ error: { code: 'body_limit', message: 'Session input exceeds 48 MiB.' } }, { status: 413, headers: privateHeaders })
    }
    const signal = AbortSignal.any([request.signal, AbortSignal.timeout(170000)])
    const response = await fetch(apiURL(path), { method: 'POST', headers, body, redirect: 'error', signal })
    const returned = new Headers(privateHeaders)
    for (const name of ['Content-Type', 'Retry-After']) {
      const value = response.headers.get(name)
      if (value !== null) returned.set(name, value)
    }
    returned.set('X-Accel-Buffering', 'no')
    if (!response.body) { release(); return new Response(null, { status: response.status, headers: returned }) }
    const reader = response.body.getReader()
    let used = 0
    let finished = false
    let output: ReadableStreamDefaultController<Uint8Array> | undefined
    const finish = () => { finished = true; signal.removeEventListener('abort', abort); release() }
    const abort = () => {
      if (finished) return
      finish()
      void reader.cancel().catch(() => {})
      output?.error(signal.reason ?? new Error('Session transport aborted.'))
    }
    const stream = new ReadableStream<Uint8Array>({
      start(controller) {
        output = controller
        signal.addEventListener('abort', abort, { once: true })
        if (signal.aborted) abort()
      },
      async pull(controller) {
        if (finished) return
        try {
          const next = await reader.read()
          if (finished) return
          if (next.done) { finish(); controller.close(); return }
          used += next.value.byteLength
          if (used > 52 * 1024 * 1024) throw new Error('Session output exceeded its limit.')
          controller.enqueue(next.value)
        } catch (error) {
          if (finished) return
          void reader.cancel().catch(() => {})
          finish(); controller.error(error)
        }
      },
      async cancel(reason) { try { await reader.cancel(reason) } finally { finish() } },
    })
    return new Response(stream, { status: response.status, headers: returned })
  } catch {
    release()
    return Response.json({ error: { code: 'call_interrupted', message: 'Session call outcome is uncertain. Do not replay this request.' } }, { status: 503, headers: privateHeaders })
  }
}
