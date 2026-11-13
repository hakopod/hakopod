import React from 'react'
import { createRoot } from 'react-dom/client'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { createRootRoute, createRouter, Outlet, RouterProvider } from '@tanstack/react-router'
import '../../src/styles.css'

// Browser-only, synthetic data for the Railway/Vercel migration flow.
// ?fixture=planned|running|succeeded|failed sets the state of the saved migration.
// This module never calls the original fetch.
const scenario = new URLSearchParams(location.search).get('fixture') || 'planned'
const id = 'f'.repeat(32)
const now = Date.now()
const at = (minutes: number) => new Date(now - minutes * 60000).toISOString()
const identity = {
  id: 'synthetic-migration-owner',
  name: 'Review Owner',
  email: 'owner@example.invalid',
  admin: true,
  owner: true,
  can_manage_git: true,
  credential_type: 'browser',
  permissions: ['deployments:read', 'deployments:write', 'logs:read'],
  project_roles: [{ project: 'review-project', role: 'admin' }],
  host_permissions: [],
  avatar_url: '',
  mfa_required: false,
}
const proposal = {
  spec: {
    schema_version: 1,
    name: 'storefront',
    services: {
      web: {
        image: 'docker.io/library/busybox:1.37.0@sha256:9db7b59979c38555a39def84a31fb98b5296952f9e3afd4f6f11f05b07adfab0',
        port: 3000,
        public: true,
        replicas: 2,
        secrets: { NEXTAUTH_SECRET: 'migrate-1', STRIPE_PUBLISHABLE_KEY: 'migrate-2' },
      },
      api: {
        image: 'docker.io/library/busybox:1.37.0@sha256:9db7b59979c38555a39def84a31fb98b5296952f9e3afd4f6f11f05b07adfab0',
        port: 8080,
        public: true,
        replicas: 2,
        secrets: { STRIPE_SECRET_KEY: 'migrate-3', JWT_SIGNING_KEY: 'migrate-4' },
      },
      worker: {
        image: 'ghcr.io/acme/storefront-worker:2.4.1',
        replicas: 1,
        secrets: { SENDGRID_API_KEY: 'migrate-5' },
      },
      'nightly-report': {
        image: 'ghcr.io/acme/storefront-worker:2.4.1',
        command: ['node', 'scripts/report.js'],
        job: { schedule: { cron: '0 3 * * *', timezone: 'UTC', history_limit: 1 } },
      },
    },
  },
  builds: [
    { service: 'web', provider: 'github', connection_id: 'github-default', repository: 'acme/storefront', branch: 'main', context_path: 'apps/web', mode: 'framework', preset: 'nextjs', port: 3000, public: true },
    { service: 'api', provider: 'github', connection_id: 'github-default', repository: 'acme/storefront', branch: 'main', context_path: 'apps/api', mode: 'framework', preset: 'node', port: 8080, public: true },
  ],
  databases: [
    { name: 'postgres', source: 'Postgres', engine: 'postgresql', version: '16', copy_data: true, reason: '' },
    { name: 'redis', source: 'Redis', engine: 'redis', version: '8', copy_data: true, reason: '' },
  ],
  bindings: [
    { service: 'api', variable: 'DATABASE_URL', database: 'postgres' },
    { service: 'worker', variable: 'REDIS_URL', database: 'redis' },
  ],
  secret_names: ['migrate-1', 'migrate-2', 'migrate-3', 'migrate-4', 'migrate-5'],
  domains: { 'shop.example.com': 'web', 'api.shop.example.com': 'api' },
  missing: [{ service: 'api', name: 'STRIPE_WEBHOOK_SECRET' }],
  warnings: [
    'Database redis runs version 7.2; Hakopod will create version 8.',
    'Service nightly-report runs on a cron schedule, so it becomes a Hakopod job without a port or domains.',
    'Service worker: an empty 5 GiB volume is mounted at /data; data from the source volume is not copied.',
  ],
}
const step = (name: string, status: string, message = '', items: object[] = []) => ({
  name,
  status,
  message,
  items,
  started_at: status === 'pending' ? undefined : at(4),
  finished_at: ['succeeded', 'failed', 'skipped'].includes(status) ? at(2) : undefined,
})
const steps = {
  running: [
    step('secrets', 'succeeded', '5 secrets saved.'),
    step('databases', 'succeeded', 'Both databases are ready.', [
      { name: 'postgres', status: 'succeeded', message: 'PostgreSQL 16' },
      { name: 'redis', status: 'succeeded', message: 'Redis 8' },
    ]),
    step('data', 'running', 'Copying data.', [
      { name: 'postgres', status: 'running', message: '1.2 GB of 3.4 GB' },
      { name: 'redis', status: 'succeeded', message: '18,204 keys' },
    ]),
    step('deploy', 'pending'),
    step('builds', 'pending'),
    step('domains', 'pending'),
    step('finish', 'pending'),
  ],
  succeeded: [
    step('secrets', 'succeeded', '5 secrets saved.'),
    step('databases', 'succeeded', 'Both databases are ready.'),
    step('data', 'succeeded', 'Data copied.', [
      { name: 'postgres', status: 'succeeded', message: '3.4 GB' },
      { name: 'redis', status: 'succeeded', message: '18,204 keys' },
    ]),
    step('deploy', 'succeeded', 'Revision 1 deployed.'),
    step('builds', 'succeeded', '2 pull requests opened.', [
      { name: 'web', status: 'succeeded', message: 'acme/storefront#214' },
      { name: 'api', status: 'succeeded', message: 'acme/storefront#215' },
    ]),
    step('domains', 'succeeded', 'Added as pending.', [
      { name: 'shop.example.com', status: 'pending', message: 'Waiting for DNS' },
      { name: 'api.shop.example.com', status: 'pending', message: 'Waiting for DNS' },
    ]),
    step('finish', 'succeeded', 'Token erased.'),
  ],
  failed: [
    step('secrets', 'succeeded', '5 secrets saved.'),
    step('databases', 'succeeded', 'Both databases are ready.'),
    step('data', 'failed', 'The copy of postgres stopped.', [
      { name: 'postgres', status: 'failed', message: 'connection refused by the source' },
      { name: 'redis', status: 'succeeded', message: '18,204 keys' },
    ]),
    step('deploy', 'skipped'),
    step('builds', 'skipped'),
    step('domains', 'skipped'),
    step('finish', 'succeeded', 'Token erased.'),
  ],
} as Record<string, object[]>
let migration: Record<string, unknown> = {
  id,
  project: 'review-project',
  environment: 'production',
  application_name: 'storefront',
  provider: 'railway',
  source_project_id: 'rw-project-1',
  source_project_name: 'Storefront',
  source_environment_id: 'rw-env-production',
  proposal: scenario === 'planned' ? proposal : { ...proposal, missing: [] },
  status: scenario,
  steps: steps[scenario] || [],
  error: scenario === 'failed' ? 'Copying postgres failed: the source refused the connection. Check that its TCP proxy is still on.' : '',
  application_id: scenario === 'planned' ? '' : 'synthetic-storefront',
  cancel_requested: false,
  has_token: scenario === 'planned' || scenario === 'running',
  created_at: at(6),
  started_at: scenario === 'planned' ? undefined : at(4),
  finished_at: ['succeeded', 'failed'].includes(scenario) ? at(1) : undefined,
  expires_at: new Date(now + 86400000).toISOString(),
}
// Open in the review project, as a user arriving from its Applications page would.
sessionStorage.setItem('hakopod-scope', JSON.stringify({ project: 'review-project', environment: 'production' }))
const fixture = { synthetic: true, scenario, requests: [] as string[], unknown: [] as string[], bodies: [] as unknown[] }
Object.assign(window, { __migrationFixture: fixture })

const answers: Record<string, unknown> = {
  '/api/me': identity,
  '/api/projects': {
    items: [
      {
        id: 'synthetic-project',
        name: 'review-project',
        display_name: 'Review project',
        personal: false,
        environments: [{ name: 'production' }, { name: 'staging' }],
      },
    ],
  },
  '/api/auth/status': {
    setup_required: false,
    deployment_mode: 'self-hosted',
    password: true,
    signup_enabled: false,
    providers: [],
    passkeys: true,
    totp: true,
    email_delivery: false,
  },
  '/api/license': { valid: true, plan: 'pro', state: 'active', installation_id: 'synthetic-installation', revision: 1, edition: 'pro', catalog: [] },
  '/api/security/advisories': {
    feed: { feed_enabled: true, osv_enabled: true, revision: 0, issued_at: null, expires_at: null, checked_at: null, stale: false, error: '' },
    applications: [],
    summary: { affected_applications: 0, critical: 0, high: 0 },
  },
  '/api/alarms': { items: [], next_cursor: '', summary: { active: 0, unread: 0 } },
  '/api/applications': { items: [] },
  '/api/registries': { items: [] },
  '/api/git/connections': {
    items: [
      {
        id: 'github-default',
        name: 'acme on GitHub',
        provider: 'github',
        auth_kind: 'github_app',
        revision: 1,
        enabled: true,
        account: 'acme',
        subject_id: 1,
        github_app_id: 1,
        installation_id: 1,
        legacy: false,
        configured: true,
        token_configured: true,
        private_repositories: true,
        webhook_path: '',
        status: 'ready',
        capabilities: { read_source: true, builds: true },
        updated_at: at(600),
      },
    ],
  },
}

window.fetch = async (input: RequestInfo | URL, init?: RequestInit) => {
  const request = input instanceof Request ? input : new Request(new URL(String(input), location.origin), init)
  const url = new URL(request.url)
  const method = init?.method || request.method
  fixture.requests.push(`${method} ${url.pathname}`)
  if (url.origin !== location.origin) throw new Error(`Synthetic fixture blocked ${url.origin}`)
  const body = method === 'GET' ? undefined : await request.clone().json().catch(() => undefined)
  if (body) fixture.bodies.push({ path: url.pathname, body })
  await new Promise((resolve) => setTimeout(resolve, 150))
  if (method === 'GET' && url.pathname in answers) return Response.json(answers[url.pathname])
  if (url.pathname === '/api/migrations/projects' && method === 'POST')
    return Response.json({
      projects: [
        { id: 'rw-project-1', name: 'Storefront', team: 'Acme', team_id: 'team-acme', environments: [{ id: 'rw-env-staging', name: 'staging' }, { id: 'rw-env-production', name: 'production' }] },
        { id: 'rw-project-2', name: 'Marketing Site', team: 'Acme', team_id: 'team-acme', environments: [{ id: 'rw-env-2', name: 'production' }] },
      ],
    })
  if (url.pathname === '/api/migrations' && method === 'POST') {
    migration = { ...migration, status: 'planned', steps: [], proposal }
    return Response.json(migration, { status: 201 })
  }
  if (url.pathname === '/api/migrations' && method === 'GET')
    return Response.json({
      items: [
        migration,
        { ...migration, id: 'e'.repeat(32), application_name: 'marketing', source_project_name: 'Marketing Site', provider: 'vercel', status: 'succeeded', created_at: at(60 * 26) },
      ],
    })
  if (url.pathname === `/api/migrations/${id}` && method === 'GET') return Response.json(migration)
  if (url.pathname === `/api/migrations/${id}/variables` && method === 'PUT') {
    const values = (body as { values?: Record<string, Record<string, string>> })?.values || {}
    migration = {
      ...migration,
      proposal: { ...proposal, missing: proposal.missing.filter((item) => !values[item.service]?.[item.name]) },
    }
    return Response.json(migration)
  }
  if (url.pathname === `/api/migrations/${id}/start` && method === 'POST') {
    migration = { ...migration, status: 'running', steps: steps.running }
    return Response.json(migration)
  }
  if (url.pathname === `/api/migrations/${id}/cancel` && method === 'POST') {
    migration = { ...migration, cancel_requested: true }
    return Response.json(migration)
  }
  fixture.unknown.push(`${method} ${url.pathname}`)
  return Response.json({ error: { code: 'not_found', message: 'Synthetic fixture: not part of this review.' } }, { status: 404 })
}

const [{ DashboardShell }, migrate, create] = await Promise.all([
  import('../../src/components/shell'),
  import('../../src/routes/applications.migrate'),
  import('../../src/routes/applications.new'),
])
const root = createRootRoute({ component: () => <DashboardShell><Outlet /></DashboardShell> })
const routes = [
  migrate.Route.update({ id: '/applications/migrate', path: '/applications/migrate', getParentRoute: () => root } as never),
  create.Route.update({ id: '/applications/new', path: '/applications/new', getParentRoute: () => root } as never),
]
const router = createRouter({ routeTree: root.addChildren(routes as never), defaultPreload: false })
const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false, refetchOnWindowFocus: false, gcTime: 0 } } })
createRoot(document.getElementById('root')!).render(
  <QueryClientProvider client={queryClient}>
    <RouterProvider router={router} />
  </QueryClientProvider>,
)
