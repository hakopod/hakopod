import React from 'react'
import { createRoot } from 'react-dom/client'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { createRootRoute, createRouter, Outlet, RouterProvider } from '@tanstack/react-router'
import '../../src/styles.css'

// DEVELOPMENT ONLY. Intercept all API calls before loading product routes.
// Artificial results are UI evidence, never database acceptance evidence.
const scenario = new URLSearchParams(location.search).get('fixture') || 'success'
const databaseID = 'cccccccccccccccccccccccccccccccc'
const now = new Date().toISOString()
const permissions = scenario === 'denied' ? [] : scenario === 'write-only' ? ['databases:write-query'] : scenario === 'read-only' ? ['databases:query'] : ['databases:query', 'databases:write-query', 'deployments:write']
const identity = {
  id: 'artificial-sql-review-user', name: 'UI reviewer', email: 'review@example.invalid',
  admin: false, owner: false, project: '', environment: '', credential_type: 'browser',
  permissions: ['deployments:read', 'logs:read', ...permissions],
  project_roles: [{ project: 'review-project', role: 'developer', permissions: ['deployments:read', 'logs:read', ...permissions] }],
  host_permissions: [], avatar_url: '', mfa_required: false,
}
const database = {
  id: databaseID, project: 'review-project', environment: 'development', revision: 4,
  status: scenario === 'unready' ? 'creating' : 'ready',
  spec: { schema_version: 1, name: 'review-database-with-a-deliberately-long-name', engine: scenario === 'unsupported' ? 'mysql' : 'postgresql', version: '17', mode: 'standalone', replicas: 1, shards: 1, cpu: '100m', memory: '256Mi', storage_gib: 1, tls: { mode: 'required' } },
  observation: { observed_at: now, revision: 4, status: 'ready', message: 'Artificial observation; no database was queried.', members: [], slots_healthy: true, endpoints: [{ purpose: 'read_write', host: 'synthetic-database.example.invalid', port: 5432 }], tls: { required: true, verified: false, plaintext_rejected: false, message: 'Artificial TLS policy only.' } },
  created_at: now, updated_at: now,
  ...(scenario === 'recovery' ? { recovery: { inspected_at: '' } } : {}),
}
const fixture = {
  synthetic: true, ready: false, scenario, database, identity,
  resultMode: scenario, blocked: [] as string[], requests: [] as unknown[], submissions: [] as unknown[],
  releaseRead: undefined as undefined | (() => void), releaseQuery: undefined as undefined | (() => void),
  invalidate: () => {},
}
Object.assign(window, { __queryFixture: fixture })
const fail = (message: string) => ({ error: { code: 'fixture_unavailable', message: `Artificial fixture: ${message}` } })
window.fetch = async (input: RequestInfo | URL, init?: RequestInit) => {
  const request = input instanceof Request ? input : new Request(new URL(String(input), location.origin), init)
  const url = new URL(request.url)
  const method = init?.method || request.method
  let data: unknown, status = 200
  if (url.origin === location.origin && method === 'GET') {
    if (url.pathname === '/api/me') data = identity
    else if (url.pathname === '/api/projects') data = { items: [{ id: 'review-project-id', name: database.project, display_name: 'Review project', personal: false, environments: [{ name: 'development' }] }] }
    else if (url.pathname === '/api/auth/status') data = { setup_required: false, deployment_mode: 'self-hosted', password: true, signup_enabled: false, providers: [], passkeys: true, totp: true, email_delivery: false }
    else if (url.pathname === '/api/license') data = { valid: false, plan: 'free', state: 'inactive', installation_id: 'review-installation', revision: 1, edition: 'community', catalog: [] }
    else if (url.pathname === '/api/alarms') data = { items: [], next_cursor: '', summary: { active: 0, unread: 0 } }
    else if (url.pathname === `/api/databases/${databaseID}`) {
      if (scenario === 'loading') await new Promise<void>((resolve) => { fixture.releaseRead = resolve })
      if (scenario === 'load-error') { status = 503; data = fail('The database could not be loaded.') }
      else data = database
    }
    else if (url.pathname === '/api/databases') data = { items: [database], next_cursor: '' }
    else if (url.pathname === `/api/databases/${databaseID}/metrics`) data = { database_id: databaseID, samples: [], truncated: false }
    else if (url.pathname === `/api/databases/${databaseID}/operations`) data = { items: [], next_cursor: '' }
    else if (url.pathname === `/api/databases/${databaseID}/connections`) data = { items: [], next_cursor: '' }
  }
  if (url.origin === location.origin && method === 'POST' && url.pathname === `/api/databases/${databaseID}/query`) {
    const body = await request.json()
    fixture.submissions.push(structuredClone(body))
    if (fixture.resultMode === 'hold') await new Promise<void>((resolve) => { fixture.releaseQuery = resolve })
    if (fixture.resultMode === 'network-error') throw new Error('Artificial connection ended')
    if (fixture.resultMode === 'failed' || fixture.resultMode === 'unknown') {
      status = 503; data = { ...fail(fixture.resultMode === 'unknown' ? 'Commit outcome unknown. Check the database before retrying.' : 'Query failed. Inputs are preserved.'), operation_id: 'artificial-operation-0001', outcome: fixture.resultMode === 'unknown' ? 'unknown' : 'rolled_back' }
    } else data = {
      operation_id: 'artificial-operation-0001', database_id: databaseID, read_only: body.read_only,
      columns: [{ name: 'id', type_oid: 20 }, { name: 'nullable_value', type_oid: 25 }, { name: 'large_numeric', type_oid: 1700 }, { name: 'long_text_for_overflow_review', type_oid: 25 }],
      rows: fixture.resultMode === 'empty' ? [] : [['1', null, '9007199254740993123456789', 'Artificial result text. '.repeat(28)]],
      rows_affected: body.read_only ? 1 : 2, truncated: fixture.resultMode === 'truncated', outcome: body.read_only ? 'read' : 'committed',
    }
  }
  if (data === undefined) {
    const blocked = `${method} ${url.origin}${url.pathname}`
    fixture.blocked.push(blocked)
    throw new Error(`Artificial fixture blocked unexpected request: ${blocked}`)
  }
  fixture.requests.push({ method, path: url.pathname, status })
  if (fixture.requests.length > 256) fixture.requests.shift()
  return new Response(JSON.stringify(data), { status, headers: { 'Content-Type': 'application/json' } })
}
const [{ DashboardShell }, queryModule, detailModule] = await Promise.all([
  import('../../src/components/shell'), import('../../src/routes/databases.$databaseId.query'), import('../../src/routes/databases.$databaseId'),
])
const root = createRootRoute({ component: () => <DashboardShell><Outlet /></DashboardShell> })
const detailRoute = detailModule.Route.update({ id: '/databases/$databaseId', path: '/databases/$databaseId', getParentRoute: () => root } as never)
const queryRoute = queryModule.Route.update({ id: '/query', path: '/query', getParentRoute: () => detailRoute } as never)
const router = createRouter({ routeTree: root.addChildren([detailRoute.addChildren([queryRoute])]), defaultPreload: false })
const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false, refetchOnWindowFocus: false, gcTime: 0 } } })
fixture.invalidate = () => { void queryClient.invalidateQueries({ queryKey: ['managed-database'] }) }
createRoot(document.getElementById('root')!).render(<QueryClientProvider client={queryClient}><RouterProvider router={router} /></QueryClientProvider>)
fixture.ready = true
