// Development-only marketing screenshot fixture. Product routes never import it and it is
// not part of the production build. Every /api/* request is answered with artificial data.
// Open /scripts/marketing-ui-fixture.html?view=app|catalog|deployment&theme=paper|dark&label=0
import '../src/styles.css'
import { createRoot } from 'react-dom/client'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import {
  createMemoryHistory,
  createRootRoute,
  createRouter,
  Outlet,
  RouterProvider,
} from '@tanstack/react-router'
import { DashboardShell } from '../src/components/shell'
import { Route as ApplicationRoute } from '../src/routes/applications.$applicationId'
import { Route as TemplatesRoute } from '../src/routes/templates'
import { Route as DeploymentRoute } from '../src/routes/deployments.$deploymentId'
// The real embedded catalog; the API serves it the same way (internal/spec/templates.go).
import catalog from '../../templates/catalog.json'

const query = new URLSearchParams(window.location.search)
const view = query.get('view') || 'app'
const theme = query.get('theme') === 'dark' ? 'dark' : 'light'
const root = document.documentElement
root.dataset.theme = theme
root.dataset.edition = 'self-hosted'
root.className = theme

const catalogSearch = new URLSearchParams()
for (const key of ['q', 'category']) if (query.get(key)) catalogSearch.set(key, query.get(key)!)
const routes: Record<string, string> = {
  app: '/applications/app-shop',
  catalog: `/templates${catalogSearch.size ? `?${catalogSearch}` : ''}`,
  deployment: '/deployments/dep-shop-42',
  topology: '/applications/app-shop?tab=topology',
}

// Read window.fixtureUnhandled to list requests this fixture does not answer.
const unhandled: string[] = ((window as any).fixtureUnhandled = [])
const originalFetch = window.fetch.bind(window)
window.fetch = async (input, init) => {
  const url = new URL(
    typeof input === 'string' ? input : input instanceof URL ? input.href : input.url,
    window.location.origin,
  )
  if (!url.pathname.startsWith('/api/')) return originalFetch(input, init)
  const method = (init?.method || (input instanceof Request ? input.method : 'GET')).toUpperCase()
  const body = respond(method, url)
  if (body === notFound)
    return Response.json({ error: { code: 'not_found', message: 'Not found' } }, { status: 404 })
  if (body !== undefined) return Response.json(body)
  unhandled.push(`${method} ${url.pathname}${url.search}`)
  console.warn(`[fixture] unhandled ${method} ${url.pathname}${url.search}`)
  return Response.json(
    { error: { code: 'fixture_unhandled', message: `Unhandled fixture request ${url.pathname}` } },
    { status: 404 },
  )
}

const now = Date.now()
const ago = (minutes: number) => new Date(now - minutes * 60000).toISOString()
// Stable, random-looking sha256-shaped digests (FNV-1a per 8-hex block).
const digest = (seed: string) =>
  Array.from({ length: 8 }, (_, block) => {
    let hash = 0x811c9dc5
    for (const char of `${seed}:${block}`)
      hash = Math.imul(hash ^ char.charCodeAt(0), 0x01000193) >>> 0
    hash = Math.imul(hash ^ (hash >>> 15), 0x2c1b3c6d) >>> 0
    return ((hash ^ (hash >>> 13)) >>> 0).toString(16).padStart(8, '0')
  }).join('')
const images = {
  web: `docker.io/library/node:22.12-alpine@sha256:${digest('shop-web')}`,
  api: `ghcr.io/example/shop-api:1.14.2@sha256:${digest('shop-api')}`,
  worker: `ghcr.io/example/shop-worker:1.14.2@sha256:${digest('shop-worker')}`,
}

const identity = {
  id: 'user-maya',
  name: 'Maya Lindqvist',
  email: 'maya@example.com',
  admin: true,
  owner: true,
  permissions: ['*'],
  project: '',
  environment: '',
  credential_type: 'browser',
  project_roles: [],
  mfa_verified: true,
  avatar_style: 'initials',
  avatar_seed: 'maya',
  can_manage_backups: true,
  can_manage_keys: true,
  can_manage_previews: true,
  can_manage_git: true,
  can_manage_applications: true,
}
const projects = {
  items: [
    {
      id: 'demo',
      name: 'demo',
      display_name: 'Demo',
      description: 'Storefront and internal tools',
      environments: [{ name: 'prod' }, { name: 'staging' }],
    },
    {
      id: 'platform',
      name: 'platform',
      display_name: 'Platform',
      environments: [{ name: 'prod' }],
    },
  ],
}
const license = {
  installation_id: 'inst-7f3c2a',
  revision: 3,
  plan: 'pro',
  state: 'active',
  valid: true,
  issuer_configured: true,
  licensed_to: 'Example Ltd',
  license_id: 'lic-2026-0412',
  expires_at: ago(-60 * 24 * 200),
  sequence: 3,
  features: [],
  catalog: [],
}

const shopSpec = {
  schema_version: 1,
  name: 'shop',
  services: {
    web: {
      image: images.web,
      port: 3000,
      public: true,
      replicas: 2,
      healthcheck: '/healthz',
      size: 'small',
      depends_on: ['api'],
      env: { API_URL: 'http://api:8080', NODE_ENV: 'production' },
    },
    api: {
      image: images.api,
      port: 8080,
      replicas: 2,
      healthcheck: '/readyz',
      size: 'small',
      env: { LOG_LEVEL: 'info' },
      bindings: {
        DATABASE_URL: { protocol: 'postgres', managed_database: 'shop-db', endpoint: 'read_write' },
      },
    },
    worker: {
      image: images.worker,
      replicas: 1,
      size: 'small',
      command: ['node', 'dist/worker.js'],
      bindings: {
        DATABASE_URL: { protocol: 'postgres', managed_database: 'shop-db', endpoint: 'read_write' },
      },
    },
  },
  domains: { 'shop.example.com': 'web' },
}
const observed = (minutes: number, spec = shopSpec, revision = 42) => ({
  status: 'healthy',
  observed_at: ago(minutes),
  revision,
  services: [
    {
      name: 'web',
      status: 'ready',
      ready: 2,
      desired: 2,
      image: spec.services.web.image,
      images: [spec.services.web.image],
      url: 'https://shop.example.com',
      internal_address: 'web.demo-prod.svc:3000',
    },
    {
      name: 'api',
      status: 'ready',
      ready: spec.services.api.replicas,
      desired: spec.services.api.replicas,
      image: spec.services.api.image,
      images: [spec.services.api.image],
      internal_address: 'api.demo-prod.svc:8080',
    },
    {
      name: 'worker',
      status: 'ready',
      ready: 1,
      desired: 1,
      image: spec.services.worker.image,
      images: [spec.services.worker.image],
    },
  ],
})
const image = (repository: string, tag: string) =>
  `${repository}:${tag}@sha256:${digest(repository + tag)}`
type ShopSpec = typeof shopSpec
const withService = (
  spec: ShopSpec,
  name: keyof ShopSpec['services'],
  patch: Record<string, unknown>,
): ShopSpec => ({
  ...spec,
  services: { ...spec.services, [name]: { ...spec.services[name], ...patch } },
})
// Newest first. Each step undoes that revision's change to derive the spec before it,
// so the activity list and deployment plan show believable, consistent diffs.
const history: {
  revision: number
  finished: number
  minutes: number
  undo: (spec: ShopSpec) => ShopSpec
}[] = [
  {
    revision: 42,
    finished: 38,
    minutes: 2.6,
    undo: (spec) =>
      withService(
        withService(spec, 'api', {
          image: image('ghcr.io/example/shop-api', '1.14.1'),
          env: { LOG_LEVEL: 'debug' },
        }),
        'worker',
        { image: image('ghcr.io/example/shop-worker', '1.14.1') },
      ),
  },
  {
    revision: 41,
    finished: 60 * 6,
    minutes: 3.2,
    undo: (spec) =>
      withService(spec, 'web', { image: image('docker.io/library/node', '22.11-alpine') }),
  },
  {
    revision: 40,
    finished: 60 * 19,
    minutes: 1.8,
    undo: (spec) => withService(spec, 'api', { replicas: 1 }),
  },
  {
    revision: 39,
    finished: 60 * 46,
    minutes: 2.9,
    undo: (spec) =>
      withService(
        withService(spec, 'api', { image: image('ghcr.io/example/shop-api', '1.14.0') }),
        'worker',
        { image: image('ghcr.io/example/shop-worker', '1.14.0') },
      ),
  },
  {
    revision: 38,
    finished: 60 * 74,
    minutes: 2.1,
    undo: (spec) => withService(spec, 'web', { env: { API_URL: 'http://api:8080' } }),
  },
  { revision: 37, finished: 60 * 120, minutes: 4.4, undo: (spec) => spec },
]
const specs = new Map<number, ShopSpec>()
history.reduce((spec, step) => (specs.set(step.revision, spec), step.undo(spec)), shopSpec)
const deployments = history.map((step) => ({
  id: `dep-shop-${step.revision}`,
  application_id: 'app-shop',
  revision: step.revision,
  status: 'succeeded',
  result: observed(step.finished, specs.get(step.revision), step.revision),
  error: '',
  created_at: ago(step.finished + step.minutes + 0.1),
  started_at: ago(step.finished + step.minutes),
  finished_at: ago(step.finished),
}))
const application = {
  id: 'app-shop',
  name: 'shop',
  display_name: 'Shop',
  project: 'demo',
  environment: 'prod',
  revision: 42,
  metadata_revision: 2,
  status: 'healthy',
  spec: shopSpec,
  observed: observed(1),
  created_at: ago(60 * 24 * 40),
  updated_at: ago(38),
  deployments,
  service_display_names: { web: 'Web', api: 'API', worker: 'Worker' },
}

const event = (id: number, minutes: number, type: string, message: string, service = '') => ({
  id,
  time: ago(minutes),
  type,
  message,
  service,
})
const rolling = 'Rolling update started; old replicas stay ready until new ones pass health checks'
function deploymentEvents(revision: number, started: number, duration: number) {
  const before = specs.get(revision - 1)
  const after = specs.get(revision)!
  const changed = (['api', 'worker', 'web'] as const).filter(
    (name) =>
      !before || JSON.stringify(before.services[name]) !== JSON.stringify(after.services[name]),
  )
  const step = (duration - 0.5) / Math.max(changed.length, 1)
  let id = revision * 100
  const events = [
    event(++id, started, 'running', 'Reconciling immutable application release'),
    event(++id, started - 0.05, 'resolved', 'All container images resolved to immutable digests'),
    event(
      ++id,
      started - 0.15,
      'policy',
      'Namespace, resource budgets and network isolation policies applied before workload changes',
    ),
  ]
  changed.forEach((name, index) => {
    const at = started - 0.3 - step * index
    if (after.services[name].bindings)
      events.push(
        event(
          ++id,
          at,
          'binding_ready',
          'All desired pods are ready with the current database binding snapshot.',
          name,
        ),
      )
    events.push(event(++id, at - 0.05, 'applying', rolling, name))
    events.push(
      event(
        ++id,
        at - step + 0.1,
        'ready',
        'All desired replicas are ready on the new revision',
        name,
      ),
    )
  })
  events.push(
    event(++id, started - duration, 'succeeded', 'Every service is ready; release completed'),
  )
  return events
}
const deploymentDetails: Record<string, unknown> = Object.fromEntries(
  history.map((step, index) => [
    `dep-shop-${step.revision}`,
    {
      ...deployments[index],
      identity_id: identity.id,
      spec: specs.get(step.revision),
      resolved_spec: specs.get(step.revision),
      events: deploymentEvents(step.revision, step.finished + step.minutes, step.minutes),
      provenance: {},
    },
  ]),
)

const MiB = 1024 * 1024
const runtime = (service: 'web' | 'api' | 'worker') => {
  const shape = {
    web: { pods: 2, cpu: 38, memory: 142 },
    api: { pods: 2, cpu: 61, memory: 188 },
    worker: { pods: 1, cpu: 17, memory: 96 },
  }[service]
  const nodes = ['hk-node-1', 'hk-node-2']
  const suffix = ['7c9f4b6d5-x2kqp', '7c9f4b6d5-m8wzt']
  const pods = Array.from({ length: shape.pods }, (_, index) => ({
    name: `${service}-${suffix[index]}`,
    phase: 'Running',
    ready: true,
    node_name: nodes[index],
    pod_ip: `10.42.${index + 1}.${service === 'web' ? 21 : service === 'api' ? 34 : 47}`,
    created_at: ago(39),
    conditions: [{ type: 'Ready', status: 'True', last_transition_time: ago(38) }],
    containers: [
      {
        name: service,
        ready: true,
        restarts: 0,
        state: 'running',
        image: images[service],
        image_id: images[service],
        resources: {
          requests: { cpu: '250m', memory: '256Mi' },
          limits: { cpu: '1', memory: '512Mi' },
        },
        cpu_millicores: shape.cpu / shape.pods + index * 3,
        memory_bytes: Math.round((shape.memory / shape.pods + index * 6) * MiB),
      },
    ],
    events: [],
  }))
  return {
    application_id: 'app-shop',
    service,
    observed_at: ago(0.2),
    metrics: {
      available: true,
      cpu_millicores: shape.cpu,
      memory_bytes: shape.memory * MiB,
      sampled_at: ago(0.3),
      window_seconds: 60,
      pods_sampled: shape.pods,
      pods_expected: shape.pods,
    },
    pods,
    truncated: false,
  }
}

const security = {
  application_id: 'app-shop',
  application_name: 'shop',
  project: 'demo',
  environment: 'prod',
  deployment_id: 'dep-shop-42',
  revision: 42,
  checked_at: ago(12),
  evaluated_at: ago(12),
  findings: [],
  unknown_services: [],
  incomplete: [],
  auto_patch: true,
  approved_by: identity.name,
  approval_expires_at: null,
  policy_revision: 2,
  attempts: [],
  can_write: true,
}

const shopDatabase = () => ({
  id: 'shop-db',
  project: 'demo',
  environment: 'prod',
  revision: 6,
  status: 'healthy',
  spec: {
    schema_version: 1,
    name: 'shop-db',
    engine: 'postgresql',
    version: '17',
    mode: 'cluster',
    replicas: 2,
    shards: 1,
    cpu: '500m',
    memory: '1Gi',
    storage_gib: 20,
  },
  observation: {
    // databaseHealth treats observations older than 30 seconds as stale, so stamp each response.
    observed_at: new Date(Date.now() - 4000).toISOString(),
    revision: 6,
    status: 'healthy',
    message: '',
    primary: 'shop-db-1',
    slots_healthy: true,
    members: [
      {
        name: 'shop-db-1',
        uid: 'b1c2d3e4-0001',
        role: 'primary',
        ready: true,
        node: 'hk-node-1',
        phase: 'Running',
        restarts: 0,
        created_at: ago(60 * 24 * 9),
      },
      {
        name: 'shop-db-2',
        uid: 'b1c2d3e4-0002',
        role: 'replica',
        ready: true,
        node: 'hk-node-2',
        phase: 'Running',
        restarts: 0,
        created_at: ago(60 * 24 * 9),
      },
    ],
    endpoints: [
      { purpose: 'read_write', host: 'shop-db-rw.demo-prod.svc', port: 5432 },
      { purpose: 'read_only', host: 'shop-db-ro.demo-prod.svc', port: 5432 },
    ],
  },
  created_at: ago(60 * 24 * 40),
  updated_at: ago(60 * 24 * 9),
})

const notFound = Symbol('not found')
function respond(method: string, url: URL): unknown {
  if (method !== 'GET') return undefined
  const path = url.pathname.slice(4)
  if (path === '/me') return identity
  if (path === '/license') return license
  if (path === '/projects') return projects
  if (path === '/auth/status')
    return {
      setup_required: false,
      password: true,
      providers: [],
      passkeys: true,
      totp: true,
      email_delivery: true,
      signup_enabled: false,
      password_recovery: true,
      deployment_mode: 'self-hosted',
    }
  if (path === '/installation/status')
    return {
      current_version: '0.1.0-alpha.78',
      latest_version: '0.1.0-alpha.78',
      update_available: false,
      checked_at: ago(25),
      check_error: '',
      operation: { status: 'idle', message: '', version: '' },
    }
  if (path === '/alarms') return { items: [], next_cursor: '', summary: { active: 0, unread: 0 } }
  if (path === '/security/advisories')
    return {
      feed: {
        feed_enabled: true,
        osv_enabled: true,
        revision: 118,
        issued_at: ago(90),
        expires_at: ago(-60 * 24),
        checked_at: ago(12),
        stale: false,
        error: '',
      },
      applications: [security],
      summary: { affected_applications: 0, critical: 0, high: 0 },
    }
  if (path === '/templates')
    return {
      items: catalog.map((item) => ({ config_fields: [], workload_requirements: [], ...item })),
    }
  if (path === '/databases') return { items: [shopDatabase()] }
  if (path === '/applications/app-shop') return application
  if (path === '/applications/app-shop/draft') return notFound
  if (path === '/applications/app-shop/security') return security
  if (
    [
      '/applications/app-shop/volume-resizes',
      '/applications/app-shop/service-moves',
      '/applications/app-shop/actions',
    ].includes(path)
  )
    return { items: [] }
  if (path === '/applications/app-shop/domains')
    return {
      items: [
        {
          hostname: 'shop.example.com',
          service: 'web',
          active: true,
          verified: true,
          verification_name: '_hakopod.shop.example.com',
          verification_value: 'hk-verify-5d1e8a',
          verified_at: ago(60 * 24 * 12),
          target: 'ingress.example.net',
        },
      ],
      expected_revision: 42,
    }
  const runtimeMatch = path.match(/^\/applications\/app-shop\/services\/(web|api|worker)\/runtime$/)
  if (runtimeMatch) return runtime(runtimeMatch[1] as 'web' | 'api' | 'worker')
  const deploymentMatch = path.match(/^\/deployments\/([\w-]+)$/)
  if (deploymentMatch && deploymentDetails[deploymentMatch[1]])
    return deploymentDetails[deploymentMatch[1]]
  return undefined
}

// Presentation only: open the deployment log phases (or another tab) the way a reviewer would
// after the page loads. The product itself starts routine successful phases collapsed.
if (view === 'deployment') {
  const tab = query.get('tab')
  const started = Date.now()
  const timer = window.setInterval(() => {
    const phases = [
      ...document.querySelectorAll<HTMLButtonElement>(
        '.inspect-phase-header[aria-expanded="false"]',
      ),
    ]
    const trigger =
      tab &&
      [...document.querySelectorAll<HTMLElement>('.tab-trigger')].find(
        (item) => item.textContent?.trim().toLowerCase() === tab,
      )
    if (tab ? trigger : phases.length) {
      if (trigger) trigger.dispatchEvent(new MouseEvent('mousedown', { bubbles: true, button: 0 }))
      else phases.forEach((button) => button.click())
      window.clearInterval(timer)
    } else if (Date.now() - started > 8000) window.clearInterval(timer)
  }, 100)
}

const rootRoute = createRootRoute({
  component: () => (
    <>
      {query.get('label') !== '0' && (
        <div
          role="status"
          style={{
            position: 'fixed',
            right: 12,
            bottom: 12,
            zIndex: 1000,
            padding: '4px 10px',
            borderRadius: 999,
            font: '600 11px/1.4 system-ui',
            background: '#b42318',
            color: '#fff',
          }}
        >
          Fixture data
        </div>
      )}
      <DashboardShell>
        <Outlet />
      </DashboardShell>
    </>
  ),
})
const routeTree = rootRoute.addChildren([
  ApplicationRoute.update({
    id: '/applications/$applicationId',
    path: '/applications/$applicationId',
    getParentRoute: () => rootRoute,
  } as any),
  TemplatesRoute.update({
    id: '/templates',
    path: '/templates',
    getParentRoute: () => rootRoute,
  } as any),
  DeploymentRoute.update({
    id: '/deployments/$deploymentId',
    path: '/deployments/$deploymentId',
    getParentRoute: () => rootRoute,
  } as any),
])
const router = createRouter({
  routeTree,
  history: createMemoryHistory({ initialEntries: [routes[view] || routes.app] }),
})
const cache = new QueryClient({
  defaultOptions: { queries: { retry: false, staleTime: Infinity, refetchOnWindowFocus: false } },
})

createRoot(document.getElementById('root')!).render(
  <QueryClientProvider client={cache}>
    <RouterProvider router={router} />
  </QueryClientProvider>,
)
