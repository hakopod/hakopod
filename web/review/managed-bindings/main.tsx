import React from 'react'
import { createRoot } from 'react-dom/client'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { createRootRoute, createRoute, createRouter, Outlet, RouterProvider } from '@tanstack/react-router'
import '../../src/styles.css'

// DEVELOPMENT ONLY. Every API request is intercepted before product imports.
// Password bodies are discarded and never enter the evidence request ledger.
const scenario = new URLSearchParams(location.search).get('fixture') || 'postgresql'
const applicationID = 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa'
const secondApplicationID = 'bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb'
const databaseID = 'cccccccccccccccccccccccccccccccc'
const deploymentID = 'dddddddddddddddddddddddddddddddd'
const planID = 'eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee'
const now = new Date().toISOString()
const engine = ['redis', 'mysql', 'mongodb', 'oracle', 'vitess', 'clickhouse'].find((value) => scenario.startsWith(value)) || 'postgresql'
const cluster = scenario === 'redis-cluster' || engine === 'mongodb' || engine === 'vitess'
const protocol = engine === 'postgresql' ? 'postgres' : engine
const application = {
  id: applicationID, name: 'binding-review', display_name: 'Binding review application',
  project: 'review-project', environment: 'development', revision: 6, status: 'unknown',
  spec: {
    schema_version: 1, name: 'binding-review', domains: {}, networks: {},
    services: {
      api: {
        image: `registry.example.invalid/synthetic-api@sha256:${'1'.repeat(64)}`,
        public: false, port: 8080, replicas: 1, resources: { cpu: '100m', memory: '128Mi' },
        env: { NODE_ENV: 'production' }, secrets: {},
        bindings: {
          DATABASE_URL: { managed_database: databaseID, endpoint: 'read_write', protocol: 'postgres', username: 'existing_application_user', database: 'existing_application_database', password: { ref: 'existing-database-password' }, ssl_mode: 'verify-full' },
        },
      },
      worker: {
        image: `registry.example.invalid/synthetic-worker@sha256:${'2'.repeat(64)}`,
        public: false, replicas: 1, resources: { cpu: '100m', memory: '128Mi' },
        bindings: { CACHE_URL: { managed_database: databaseID, endpoint: 'read_write', protocol: 'redis', database: '0', ssl_mode: 'verify-full' } },
      },
    },
  },
  observed: { observed_at: now, revision: 6, status: 'unknown', services: [] },
  created_at: now, updated_at: now, deployments: [],
}
const secondApplication = structuredClone(application)
secondApplication.id = secondApplicationID
secondApplication.name = 'another-review-app'
secondApplication.display_name = 'Another review application'
secondApplication.spec.name = secondApplication.name
const database = {
  id: databaseID, project: application.project, environment: application.environment,
  revision: 4, status: scenario === 'unready' ? 'creating' : 'ready',
  spec: {
    schema_version: 1, name: 'review-database-with-a-deliberately-long-name', engine,
    version: ({ redis: '7.4', mysql: '8.4', mongodb: '8.0', oracle: '23', vitess: '22', clickhouse: '25.3' } as Record<string, string>)[engine] || '17',
    mode: cluster ? 'cluster' : 'standalone', replicas: cluster ? 3 : 1, shards: 1,
    cpu: '100m', memory: '256Mi', storage_gib: 1,
    ...(scenario.endsWith('legacy') ? {} : { tls: { mode: 'required' } }),
    ...(engine === 'postgresql' ? { pooling: { mode: 'transaction', instances: 1, max_client_connections: 50, default_pool_size: 10, read_only: true } } : {}),
  },
  observation: {
    observed_at: now, revision: 4, status: 'ready', message: 'Artificial observation; no database was queried.', members: [], slots_healthy: true,
    endpoints: (cluster ? ['cluster'] : engine === 'postgresql' ? ['read_write', 'read_only', 'pooled_read_write', 'pooled_read_only'] : ['read_write']).map((purpose) => ({ purpose, host: 'synthetic-database.example.invalid', port: engine === 'redis' ? 6379 : 5432 })),
    tls: { required: !scenario.endsWith('legacy'), verified: false, plaintext_rejected: false, message: 'Artificial TLS policy only.' },
  },
  created_at: now, updated_at: now,
}
const viewer = scenario === 'denied'
const identity = {
  id: 'artificial-binding-review-user', name: 'UI reviewer', email: 'review@example.invalid',
  admin: false, owner: false, project: '', environment: '', credential_type: 'browser',
  permissions: ['deployments:read', 'logs:read', ...(viewer ? [] : ['deployments:write'])],
  project_roles: [{ project: application.project, role: viewer ? 'viewer' : 'developer' }],
  host_permissions: [], avatar_url: '', mfa_required: false,
}
const fixture = {
  synthetic: true, ready: false, scenario, application, secondApplication, database,
  planMode: 'success', secretMode: 'success', connectMode: 'error', secretsMode: scenario === 'secrets-error' ? 'error' : scenario === 'secrets-empty' ? 'empty' : 'success',
  blocked: [] as string[], requests: [] as { method: string; path: string; query: string; status: number; body?: unknown }[],
  plans: [] as unknown[], submissions: [] as unknown[], secretWrites: [] as { name: string; application: string | null; valuePresent: boolean }[],
  secrets: ['existing-database-password', 'another-database-password'],
  releaseRead: undefined as undefined | (() => void),
  releasePlan: undefined as undefined | (() => void),
  releaseSecret: undefined as undefined | (() => void),
  releaseConnect: undefined as undefined | (() => void),
  invalidate: (_key: string) => {},
}
Object.assign(window, { __bindingFixture: fixture })
const fail = (message: string, code = 'fixture_unavailable') => ({ error: { code, message: `Artificial fixture: ${message}` } })

window.fetch = async (input: RequestInfo | URL, init?: RequestInit) => {
  const request = input instanceof Request ? input : new Request(new URL(String(input), location.origin), init)
  const url = new URL(request.url)
  const method = init?.method || request.method
  let data: unknown, body: any
  let status = 200
  if (url.origin === location.origin && method === 'GET') {
    if (url.pathname === '/api/me') data = identity
    else if (url.pathname === '/api/projects') data = { items: [{ id: 'review-project-id', name: application.project, display_name: 'Review project', personal: false, environments: [{ name: 'development' }] }] }
    else if (url.pathname === '/api/auth/status') data = { setup_required: false, deployment_mode: 'self-hosted', password: true, signup_enabled: false, providers: [], passkeys: true, totp: true, email_delivery: false }
    else if (url.pathname === '/api/license') data = { valid: false, plan: 'free', state: 'inactive', installation_id: 'review-installation', revision: 1, edition: 'community', catalog: [] }
    else if (url.pathname === '/api/alarms') data = { items: [], next_cursor: '', summary: { active: 0, unread: 0 } }
    else if (url.pathname === '/api/applications') data = { items: scenario === 'empty' ? [] : [application, secondApplication], next_cursor: '' }
    else if (url.pathname === `/api/applications/${applicationID}` || url.pathname === `/api/applications/${secondApplicationID}`) {
      if (scenario === 'application-error') { status = 503; data = fail('The selected application could not be loaded.') }
      else data = url.pathname.endsWith(applicationID) ? application : secondApplication
    }
    else if (url.pathname === `/api/databases/${databaseID}`) {
      if (scenario === 'loading') await new Promise<void>((resolve) => { fixture.releaseRead = resolve })
      if (scenario === 'error') { status = 503; data = fail('The database could not be loaded.') }
      else data = database
    }
    else if (url.pathname === '/api/databases') data = { items: [database], next_cursor: '' }
    else if (url.pathname === '/api/secrets') {
      if (fixture.secretsMode === 'error') { status = 503; data = fail('Application secrets could not be loaded.') }
      else data = { items: (fixture.secretsMode === 'empty' ? [] : fixture.secrets).map((name) => ({ name, application: url.searchParams.get('application') || application.name, updated_at: now })) }
    }
    else if (url.pathname === `/api/applications/${applicationID}/services/api/runtime`) data = { application_id: applicationID, service: 'api', observed_at: now, metrics: { available: false }, pods: [], truncated: false }
    else if (url.pathname === `/api/deployments/${deploymentID}`) data = { id: deploymentID, application_id: applicationID, revision: 7, status: 'queued', spec: application.spec, result: {}, error: '', created_at: now, events: [] }
  }
  if (url.origin === location.origin && method === 'POST' && url.pathname.startsWith('/api/secrets/')) {
    const value = await request.json()
    const name = decodeURIComponent(url.pathname.slice('/api/secrets/'.length))
    fixture.secretWrites.push({ name, application: url.searchParams.get('application'), valuePresent: typeof value.value === 'string' && value.value.length > 0 })
    body = { value: '[REDACTED ARTIFICIAL PASSWORD]' }
    if (fixture.secretMode === 'hold') await new Promise<void>((resolve) => { fixture.releaseSecret = resolve })
    if (fixture.secretMode === 'error') { status = 503; data = fail('The password was not saved. Your entry is preserved.') }
    else if (fixture.secretMode === 'conflict' || fixture.secrets.includes(name)) { status = 409; data = fail('The secret name already exists. Choose another name.', 'conflict') }
    else { fixture.secrets.push(name); status = 201; data = { name, saved: true } }
  }
  if (url.origin === location.origin && method === 'POST' && url.pathname === `/api/databases/${databaseID}/connection-plan`) {
    body = await request.json()
    fixture.plans.push(structuredClone(body))
    if (fixture.planMode === 'hold') await new Promise<void>((resolve) => { fixture.releasePlan = resolve })
    if (fixture.planMode === 'error') { status = 503; data = fail('The connection review could not be prepared. Your entries are preserved.') }
    else {
      const selected = body.application_id === secondApplicationID ? secondApplication : application
      const binding = { managed_database: databaseID, protocol, endpoint: body.endpoint, ...(body.cluster_aware ? { cluster_aware: true } : {}) } as Record<string, unknown>
      for (const key of ['username', 'database', 'password', 'ssl_mode']) if (body[key] !== undefined && body[key] !== '') binding[key] = structuredClone(body[key])
      data = { id: planID, database_id: databaseID, database_name: database.spec.name, database_revision: database.revision,
        application_id: selected.id, application_name: selected.name, application_revision: selected.revision,
        service: body.service, variable: body.variable, previous_kind: 'managed binding', binding,
        expires_at: new Date(Date.now() + (fixture.planMode === 'expired' ? -1000 : fixture.planMode === 'short' ? 2500 : 600000)).toISOString(),
        warnings: ['Artificial review: existing roles and databases must already exist. No database objects are created by this connection change.'],
      }
    }
  }
  if (url.origin === location.origin && method === 'POST' && url.pathname === `/api/databases/${databaseID}/connect`) {
    body = await request.json()
    fixture.submissions.push({ body, key: request.headers.get('Idempotency-Key') })
    if (fixture.connectMode === 'hold') await new Promise<void>((resolve) => { fixture.releaseConnect = resolve })
    status = 503
    data = fail('The connection could not be applied. No redeployment was submitted.')
  }
  if (data === undefined) {
    const blocked = `${method} ${url.origin}${url.pathname}`
    fixture.blocked.push(blocked)
    throw new Error(`Artificial fixture blocked unexpected request: ${blocked}`)
  }
  fixture.requests.push({ method, path: url.pathname, query: url.search, status, ...(body ? { body } : {}) })
  if (fixture.requests.length > 256) fixture.requests.shift()
  return new Response(JSON.stringify(data), { status, headers: { 'Content-Type': 'application/json' } })
}

const [{ DashboardShell }, connectModule, applicationModule, { PageHeader, Note }] = await Promise.all([
  import('../../src/components/shell'), import('../../src/routes/databases.$databaseId.connect'),
  import('../../src/routes/applications.$applicationId'), import('../../src/components/shared'),
])
const root = createRootRoute({ component: () => <DashboardShell><Outlet /></DashboardShell> })
const routes = [
  [connectModule.Route, '/databases/$databaseId/connect'],
  [applicationModule.Route, '/applications/$applicationId'],
].map(([route, path]) => (route as typeof connectModule.Route).update({ id: path, path, getParentRoute: () => root } as never))
const destination = createRoute({ getParentRoute: () => root, path: '/databases/$databaseId', component: () => <><PageHeader title="Artificial database destination" /><Note>Only the scoped navigation target is under review.</Note></> })
const router = createRouter({ routeTree: root.addChildren([...routes, destination]), defaultPreload: false })
const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false, refetchOnWindowFocus: false, gcTime: 0 } } })
fixture.invalidate = (key) => { void queryClient.invalidateQueries({ queryKey: [key] }) }
createRoot(document.getElementById('root')!).render(<QueryClientProvider client={queryClient}><RouterProvider router={router} /></QueryClientProvider>)
fixture.ready = true
