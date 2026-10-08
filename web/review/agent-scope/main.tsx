window.addEventListener('error', (event) => {
  document.getElementById('fixture-banner')!.textContent += ` Fixture error: ${event.message}`
})
window.addEventListener('unhandledrejection', (event) => {
  document.getElementById('fixture-banner')!.textContent +=
    ` Fixture error: ${String(event.reason)}`
})
import React, { useState } from 'react'
import { createRoot } from 'react-dom/client'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { createRootRoute, createRoute, createRouter, Outlet, RouterProvider } from '@tanstack/react-router'
import './review.css'

// DEVELOPMENT ONLY. Intercept all API calls before loading product routes.
// Artificial results are UI evidence, never database acceptance evidence.
const scenario = new URLSearchParams(location.search).get('fixture') || 'success'
const fixtureEngine =
  new URLSearchParams(location.search).get('engine') ||
  (scenario === 'unsupported' ? 'redis' : 'postgresql')
const keyReview = location.pathname.startsWith('/settings')
const databaseID = 'cccccccccccccccccccccccccccccccc'
const now = new Date().toISOString()
const scopeProject = { id: 'synthetic-demo', name: 'demo', display_name: 'Demo project', revision: 1, personal: scenario === 'personal', environments: scenario === 'empty' ? [] : [{ name: 'development' }, { name: 'staging' }] }
const scopeProjects = [scopeProject]

const permissions =
  scenario === 'denied'
    ? []
    : scenario === 'write-only'
      ? ['databases:write-query']
      : scenario === 'read-only'
        ? ['databases:query']
        : ['databases:query', 'databases:write-query', 'deployments:write']
const identity = {
  id: 'artificial-sql-review-user',
  name: 'UI reviewer',
  email: 'review@example.invalid',
  admin: scenario !== 'viewer',
  can_manage_keys: keyReview,
  owner: true,
  project: '',
  environment: '',
  credential_type: 'browser',
  permissions: ['deployments:read', 'logs:read', ...permissions],
  project_roles: [
    {
      project: 'demo',
      role: scenario === 'viewer' ? 'viewer' : 'admin',
      permissions: ['deployments:read', 'logs:read', ...permissions],
    },
  ],
  host_permissions: [],
  avatar_url: '',
  mfa_required: false,
}
const database = {
  id: databaseID,
  project: 'review-project',
  environment: 'development',
  revision: 4,
  status: scenario === 'unready' ? 'creating' : 'ready',
  spec: {
    schema_version: 1,
    name: 'review-database-with-a-deliberately-long-name',
    engine: fixtureEngine,
    version: '17',
    mode: 'standalone',
    replicas: 1,
    shards: 1,
    cpu: '100m',
    memory: '256Mi',
    storage_gib: 1,
    tls: { mode: 'required' },
  },
  observation: {
    observed_at: now,
    revision: 4,
    status: 'ready',
    message: 'Artificial observation; no database was queried.',
    members: [],
    slots_healthy: true,
    endpoints: [{ purpose: 'read_write', host: 'synthetic-database.example.invalid', port: 5432 }],
    tls: {
      required: true,
      verified: false,
      plaintext_rejected: false,
      message: 'Artificial TLS policy only.',
    },
  },
  created_at: now,
  updated_at: now,
  ...(scenario === 'recovery' ? { recovery: { inspected_at: '' } } : {}),
}
const fixture = {
  synthetic: true,
  ready: false,
  scenario,
  database,
  identity,
  resultMode: scenario,
  blocked: [] as string[],
  requests: [] as unknown[],
  submissions: [] as unknown[],
  releaseRead: undefined as undefined | (() => void),
  releaseQuery: undefined as undefined | (() => void),
  invalidate: () => {},
}
Object.assign(window, { __queryFixture: fixture })
const fail = (message: string) => ({
  error: { code: 'fixture_unavailable', message: `Artificial fixture: ${message}` },
})
window.fetch = async (input: RequestInfo | URL, init?: RequestInit) => {
  const request =
    input instanceof Request ? input : new Request(new URL(String(input), location.origin), init)
  const url = new URL(request.url)
  const method = init?.method || request.method
  let data: unknown,
    status = 200
  if (url.origin === location.origin && method === 'GET') {
    if (url.pathname === '/api/me') data = identity
    else if (url.pathname === '/api/projects') data = { items: scopeProjects }
    else if (url.pathname === '/api/applications') data = { items: [], next_cursor: '' }
    else if (url.pathname === '/api/auth/status')
      data = {
        setup_required: false,
        deployment_mode: 'self-hosted',
        password: true,
        signup_enabled: false,
        providers: [],
        passkeys: true,
        totp: true,
        email_delivery: false,
      }
    else if (url.pathname === '/api/license')
      data = {
        valid: keyReview,
        plan: keyReview ? 'pro' : 'free',
        state: keyReview ? 'active' : 'inactive',
        installation_id: 'review-installation',
        revision: 1,
        edition: 'community',
        catalog: keyReview ? [{ id: 'custom_roles', enabled: true }] : [],
      }
    else if (url.pathname === '/api/keys')
      data = {
        items: [
          {
            id: 'dddddddddddddddddddddddddddddddd',
            name: 'synthetic-agent-review-key',
            project: database.project,
            environment: database.environment,
            application: '',
            permissions: ['deployments:read', 'pods:exec', 'databases:query'],
            created_at: now,
            expires_at: new Date(Date.now() + 30 * 86400000).toISOString(),
            never_expires: false,
          },
        ],
      }
    else if (url.pathname === '/api/roles')
      data = {
        items: [
          {
            id: 'custom:eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee',
            name: 'Synthetic query operator',
            revision: 1,
            permissions: ['deployments:read', 'databases:query'],
          },
        ],
      }
    else if (url.pathname === '/api/alarms')
      data = { items: [], next_cursor: '', summary: { active: 0, unread: 0 } }
    else if (url.pathname === `/api/databases/${databaseID}`) {
      if (scenario === 'loading')
        await new Promise<void>((resolve) => {
          fixture.releaseRead = resolve
        })
      if (scenario === 'load-error') {
        status = 503
        data = fail('The database could not be loaded.')
      } else data = database
    } else if (url.pathname === '/api/databases') data = { items: [database], next_cursor: '' }
    else if (url.pathname === `/api/databases/${databaseID}/metrics`)
      data = { database_id: databaseID, samples: [], truncated: false }
    else if (url.pathname === `/api/databases/${databaseID}/operations`)
      data = { items: [], next_cursor: '' }
    else if (url.pathname === `/api/databases/${databaseID}/connections`)
      data = { items: [], next_cursor: '' }
    else if (url.pathname === `/api/databases/${databaseID}/query-capabilities`)
      data = {
        engine: fixtureEngine,
        supported: scenario !== 'unsupported',
        transaction_scope: fixtureEngine === 'vitess' ? 'single_shard' : fixtureEngine === 'duckdb' || fixtureEngine === 'clickhouse' ? 'none' : 'connection',
        ...(fixtureEngine === 'vitess' ? { cross_shard_dml: false } : {}),
        read_only_supported: fixtureEngine !== 'duckdb',
        execution_modes:
          scenario === 'unsupported'
            ? []
            : fixtureEngine === 'duckdb' || fixtureEngine === 'clickhouse'
              ? ['nontransactional']
              : (fixtureEngine === 'mysql' || fixtureEngine === 'vitess')
                ? ['transaction', 'nontransactional']
                : ['transaction'],
        application_identity: 'app',
        parameter_style: (fixtureEngine === 'mysql' || fixtureEngine === 'vitess') ? '?' : '$1',
        read_only_enforcement: fixtureEngine === 'duckdb' ? 'unavailable' : 'server_transaction',
        transactional_dml: fixtureEngine !== 'duckdb' && fixtureEngine !== 'clickhouse',
        transactional_ddl: fixtureEngine === 'postgresql',
        ddl_commit:
          fixtureEngine === 'duckdb' || fixtureEngine === 'clickhouse'
            ? 'nontransactional'
            : (fixtureEngine === 'mysql' || fixtureEngine === 'vitess')
              ? 'implicit_commit'
              : 'transaction',
        cancellation: 'Artificial capability: close the connection and check unknown outcomes.',
      }
    else if (url.pathname === '/api/auth/device')
      data = {
        user_code: 'FIXTURE',
        project: '',
        environment: '',
        expires_at: new Date(Date.now() + 600000).toISOString(),
        permissions: scenario.startsWith('device-host')
          ? ['nodes:terminal']
          : ['admin', 'agent:admin', 'agent:credentials'],
        scopes:
          scenario === 'device-empty' || scenario === 'device-host-empty'
            ? []
            : scenario === 'device-host'
              ? [
                  {
                    id: 'host',
                    label: 'Artificial granted host access',
                    project: '',
                    environment: '',
                  },
                ]
              : [
                  {
                    id: 'installation',
                    label: 'Artificial review installation',
                    project: '',
                    environment: '',
                  },
                ],
      }
  }
  if (
    url.origin === location.origin &&
    method === 'POST' &&
    url.pathname === '/api/auth/device/approve'
  ) {
    status = 409
    data = fail('The artificial request expired. Your selection is preserved.')
  }
  if (
    url.origin === location.origin &&
    method === 'POST' &&
    url.pathname === `/api/databases/${databaseID}/query`
  ) {
    const body = await request.json()
    fixture.submissions.push(structuredClone(body))
    if (fixture.resultMode === 'hold')
      await new Promise<void>((resolve) => {
        fixture.releaseQuery = resolve
      })
    if (fixture.resultMode === 'network-error') throw new Error('Artificial connection ended')
    if (fixture.resultMode === 'failed' || fixture.resultMode === 'unknown') {
      status = 503
      data = {
        ...fail(
          fixture.resultMode === 'unknown'
            ? 'Commit outcome unknown. Check the database before retrying.'
            : 'Query failed. Inputs are preserved.',
        ),
        operation_id: 'artificial-operation-0001',
        outcome: fixture.resultMode === 'unknown' ? 'unknown' : 'rolled_back',
      }
    } else
      data = {
        operation_id: 'artificial-operation-0001',
        database_id: databaseID,
        read_only: body.read_only,
        columns: [
          { name: 'id', type_oid: 20 },
          { name: 'nullable_value', type_oid: 25 },
          { name: 'large_numeric', type_oid: 1700 },
          { name: 'long_text_for_overflow_review', type_oid: 25 },
        ],
        rows:
          fixture.resultMode === 'empty'
            ? []
            : [['1', null, '9007199254740993123456789', 'Artificial result text. '.repeat(28)]],
        rows_affected: body.read_only ? 1 : 2,
        truncated: fixture.resultMode === 'truncated',
        outcome: body.read_only
          ? 'read'
          : body.execution_mode === 'nontransactional'
            ? 'applied'
            : 'committed',
      }
  }
  if (
    url.origin === location.origin &&
    ['POST', 'PUT', 'DELETE'].includes(method) &&
    (/^\/api\/keys(?:\/[^/]+(?:\/rotate)?)?$/.test(url.pathname) ||
      /^\/api\/roles(?:\/[^/]+)?$/.test(url.pathname))
  ) {
    const body = await request.json().catch(() => ({}))
    fixture.submissions.push({ method, path: url.pathname, body: structuredClone(body) })
    if (scenario === 'key-failed' || scenario === 'role-failed') {
      status = 409
      data = fail('The synthetic revision changed. Entered values remain available.')
    } else if (url.pathname.startsWith('/api/keys'))
      data = {
        key: 'DEVELOPMENT_ONLY_SYNTHETIC_KEY_NOT_VALID',
        metadata: { id: 'dddddddddddddddddddddddddddddddd', ...body },
        previous_key_expires_within_seconds: 900,
      }
    else data = { id: 'custom:eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee', revision: 2, ...body }
  }
  if (url.origin === location.origin && method === 'POST' && url.pathname === '/api/projects') {
    const body = await request.json()
    fixture.submissions.push({ method, path: url.pathname, body })
    if (scenario === 'conflict') { status = 409; data = fail('The project ID is reserved. Entered values remain available.') }
    else if (scopeProjects.length >= 32) { status = 409; data = fail('Synthetic project limit reached.') }
    else { const project = { ...scopeProject, id: `synthetic-${body.name}`, name: body.name, display_name: body.display_name, personal: false, environments: [{ name: body.environment }] }; scopeProjects.push(project); data = project }
  }
  if (url.origin === location.origin && method === 'PUT' && url.pathname === '/api/projects/demo/name') {
    const body = await request.json()
    fixture.submissions.push({ method, path: url.pathname, body })
    if (scenario === 'conflict') { status = 409; data = fail('The synthetic revision changed. Entered name remains available.') }
    else { scopeProject.display_name = body.display_name; data = scopeProject }
  }
  if (url.origin === location.origin && method === 'POST' && url.pathname === '/api/projects/demo/environments') {
    const body = await request.json()
    fixture.submissions.push({ method, path: url.pathname, body })
    if (scenario === 'conflict') { status = 409; data = fail('The environment ID is reserved. Entered ID remains available.') }
    else if (scopeProject.environments.length >= 32) { status = 409; data = fail('Synthetic environment limit reached.') }
    else { scopeProject.environments.push({ name: body.name }); data = { name: body.name } }
  }
  if (url.origin === location.origin && method === 'DELETE' && url.pathname.startsWith('/api/projects/')) {
    const body = await request.json()
    fixture.submissions.push({ method, path: url.pathname, body })
    if (scenario === 'conflict') { status = 409; data = fail('The environment contains retained storage. Confirmation remains entered.') }
    else {
      const parts = url.pathname.split('/')
      if (parts.includes('environments')) scopeProject.environments = scopeProject.environments.filter(e => e.name !== parts.at(-1))
      else scopeProjects.splice(0)
      data = { deleted: true }
    }
  }
  if (data === undefined) {
    const blocked = `${method} ${url.origin}${url.pathname}`
    fixture.blocked.push(blocked)
    if (fixture.blocked.length > 64) fixture.blocked.shift()
    throw new Error(`Artificial fixture blocked unexpected request: ${blocked}`)
  }
  if (fixture.submissions.length > 64) fixture.submissions.splice(0, fixture.submissions.length - 64)
  fixture.requests.push({ method, path: url.pathname, status })
  if (fixture.requests.length > 256) fixture.requests.shift()
  return new Response(JSON.stringify(data), {
    status,
    headers: { 'Content-Type': 'application/json' },
  })
}
const [{ DashboardShell }, projectModule, indexModule, { TOMLEditor }] = await Promise.all([
  import('../../src/components/shell'),
  import('../../src/routes/projects.$project'),
  import('../../src/routes/index'),
  import('../../src/components/toml-editor'),
])
const root = createRootRoute({
  component: () => (
    <DashboardShell>
      <Outlet />
    </DashboardShell>
  ),
})
function ExpandedEditorFixture() {
  const [text, setText] = useState('schema_version = 1\nname = "development-only-review"\n\n[services.app]\nimage = "example.invalid/artificial:fixture"\n')
  return <section className="flex h-[calc(100dvh-180px)] min-h-[320px] min-w-0 flex-col" aria-label="Development-only expanded editor fixture">
    <h1 className="py-3">Expanded TOML review</h1>
    <div className="flex min-h-0 flex-1 flex-col"><TOMLEditor label="Expanded TOML configuration" value={text} onChange={setText} expanded /></div>
  </section>
}
const editorRoute = createRoute({ path: '/review/expanded-editor', getParentRoute: () => root, component: ExpandedEditorFixture })
const projectRoute = projectModule.Route.update({ id: '/projects/$project', path: '/projects/$project', getParentRoute: () => root } as never)
const indexRoute = indexModule.Route.update({ id: '/', path: '/', getParentRoute: () => root } as never)
const router = createRouter({
  routeTree: root.addChildren([projectRoute, indexRoute, editorRoute]),
  defaultPreload: false,
})
const queryClient = new QueryClient({
  defaultOptions: { queries: { retry: false, refetchOnWindowFocus: false, gcTime: 0 } },
})
fixture.invalidate = () => {
  void queryClient.invalidateQueries({ queryKey: ['managed-database'] })
}
createRoot(document.getElementById('root')!).render(
  <QueryClientProvider client={queryClient}>
    <RouterProvider router={router} />
  </QueryClientProvider>,
)
fixture.ready = true
