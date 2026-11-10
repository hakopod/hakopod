import React from 'react'
import { createRoot } from 'react-dom/client'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { createRootRoute, createRoute, createRouter, Outlet, RouterProvider } from '@tanstack/react-router'
import '../../src/styles.css'

// Browser-only, synthetic test data for the runner pool form and pool card.
// This module never calls the original fetch.
const scenario = new URLSearchParams(location.search).get('fixture') || 'fixed'
const scaled = scenario === 'scaled' || scenario === 'sleeping'
const application = {
  id: 'synthetic-runners',
  name: 'ci-runners',
  project: 'review-project',
  environment: 'development',
  revision: 3,
  status: 'ready',
  observed: { status: 'ready', revision: 3 },
  created_at: '2026-11-01T00:00:00Z',
  updated_at: '2026-11-01T00:00:00Z',
  spec: {
    schema_version: 1,
    name: 'ci-runners',
    services: {
      runner: {
        image:
          'ghcr.io/hakopod/actions-runner:2.337.0-hakopod-900500b7096ef5647f29f05d76bca5b18a04882d@sha256:9997dfd0b0b39513e193f7e51c44f1f25f3216713fba5de7a9e5812676222bd6',
        size: 'compute',
        replicas: 4,
        actions: {
          organization: 'example-team',
          credential: 'github-runner-token',
          labels: ['hakopod'],
          timeout_minutes: 60,
          ...(scaled ? { idle_seconds: 600 } : {}),
          ...(scenario === 'scaled' ? { min_replicas: 1 } : {}),
        },
      },
    },
  },
}
const identity = {
  id: 'synthetic-runner-review-owner',
  name: 'Review Owner',
  email: 'owner@example.invalid',
  admin: true,
  owner: true,
  credential_type: 'browser',
  permissions: ['deployments:read', 'deployments:write', 'logs:read'],
  project_roles: [{ project: 'review-project', role: 'admin' }],
  host_permissions: [],
  avatar_url: '',
  mfa_required: false,
}
const profile = { CPURequest: '1', CPULimit: '2', MemoryRequest: '2Gi', MemoryLimit: '4Gi' }
const fixture = { synthetic: true, scenario, requests: [] as string[], unknown: [] as string[] }
Object.assign(window, { __runnerFixture: fixture })

const answers: Record<string, unknown> = {
  '/api/me': identity,
  '/api/projects': {
    items: [
      {
        id: 'synthetic-project',
        name: 'review-project',
        display_name: 'Review project',
        personal: false,
        environments: [{ name: 'development' }],
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
  '/api/license': {
    valid: true,
    plan: 'pro',
    state: 'active',
    installation_id: 'synthetic-installation',
    revision: 1,
    edition: 'pro',
    catalog: [{ id: 'managed_actions', name: 'Managed Actions', plan: 'pro', description: '', enabled: true }],
  },
  '/api/security/advisories': {
    feed: { feed_enabled: true, osv_enabled: true, revision: 0, issued_at: null, expires_at: null, checked_at: null, stale: false, error: '' },
    applications: [],
    summary: { affected_applications: 0, critical: 0, high: 0 },
  },
  '/api/alarms': { items: [], next_cursor: '', summary: { active: 0, unread: 0 } },
  '/api/applications': { items: [application] },
  '/api/applications/synthetic-runners': application,
  '/api/actions/capabilities': {
    licensed: true,
    runtime_ready: true,
    message: '',
    runner_image: application.spec.services.runner.image,
    resource_profiles: { compute: profile, small: profile, large: profile },
    providers: [
      {
        provider: 'github',
        lifecycle: 'ephemeral',
        available: true,
        image: application.spec.services.runner.image,
        minimum_resources: { cpu_request: '200m', cpu_limit: '1', memory_request: '768Mi', memory_limit: '4Gi', workspace_gib: 2 },
        cache: { persistent: true, backend: 'github', reason: 'Configure actions/cache or a tool’s cache integration in your workflow.' },
        build: { native_architectures: ['amd64', 'arm64'], cross_architecture: true },
        isolation: { single_job: true, manager_credentials_isolated: true },
        cancellation_scope: 'none',
      },
    ],
  },
  '/api/placement/nodes': {
    items: [{ name: 'runner-node-1', architecture: 'amd64', available: true, reason: '' }],
  },
  '/api/settings/actions-capacity': {
    external_id: 'hakopod-0123456789abcdef0123456789abcdef',
    installation_id: 'fedcba9876543210fedcba9876543210',
    revision: 1,
    images: {},
    capacities: [
      { name: 'aws-spot', cloud: 'aws', region: 'us-east-1', role_arn: 'arn:aws:iam::123456789012:role/hakopod-actions', subnet_ids: ['subnet-0abc1234'], security_group_ids: ['sg-0abc1234'], instance_types: ['c7i.xlarge', 'c6i.xlarge', 'm7i.xlarge'], architecture: 'amd64', spot: true, disk_gib: 100, max_instances: 20, environments: ['*'] },
      { name: 'gcp-spot', cloud: 'gcp', region: 'us-central1', project: 'review-project', zones: ['us-central1-a'], service_account: 'hakopod-actions@review-project.iam.gserviceaccount.com', subnet_ids: ['default'], instance_types: ['t2a-standard-2'], architecture: 'arm64', spot: true, max_instances: 5, environments: ['*'] },
    ],
  },
  '/api/applications/synthetic-runners/actions': {
    items: [
      {
        pool: {
          application_id: 'synthetic-runners',
          service: 'runner',
          project: 'review-project',
          environment: 'development',
          application_name: 'ci-runners',
          revision: 3,
          config: application.spec.services.runner,
          removed: false,
          message: '',
          updated_at: new Date().toISOString(),
        },
        slots: [],
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
  if (method === 'GET' && url.pathname in answers) return Response.json(answers[url.pathname])
  if (method === 'POST' && url.pathname === '/api/plan') {
    const body = await request.clone().json().catch(() => ({}))
    const after = body?.spec?.services?.runner?.actions || {}
    const before = application.spec.services.runner.actions as Record<string, unknown>
    const changes = ['idle_seconds', 'min_replicas']
      .filter((field) => before[field] !== after[field])
      .map((field) => ({ service: 'runner', field: `actions.${field}`, before: before[field] ?? null, after: after[field] ?? null, sensitive: false }))
    return Response.json({ application_id: application.id, expected_revision: application.revision, spec: body.spec, changes, warnings: [] })
  }
  fixture.unknown.push(`${method} ${url.pathname}`)
  return Response.json({ error: { code: 'not_found', message: 'Synthetic fixture: not part of this review.' } }, { status: 404 })
}

const [{ DashboardShell }, configure, { ManagedActionsStatus }, { FormPage }] = await Promise.all([
  import('../../src/components/shell'),
  import('../../src/routes/applications.$applicationId.configure'),
  import('../../src/components/managed-actions-status'),
  import('../../src/components/form-page'),
])
const root = createRootRoute({ component: () => <DashboardShell><Outlet /></DashboardShell> })
const configureRoute = configure.Route.update({
  id: '/applications/$applicationId/configure',
  path: '/applications/$applicationId/configure',
  getParentRoute: () => root,
} as never)
const statusRoute = createRoute({
  getParentRoute: () => root,
  path: '/status',
  component: () => (
    <FormPage title="Runner pool" description="Synthetic pool card." breadcrumbs={[]}>
      <ManagedActionsStatus application={application as never} service="runner" />
    </FormPage>
  ),
})
const router = createRouter({ routeTree: root.addChildren([configureRoute, statusRoute] as never), defaultPreload: false })
const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false, refetchOnWindowFocus: false, gcTime: 0 } } })
createRoot(document.getElementById('root')!).render(
  <QueryClientProvider client={queryClient}>
    <RouterProvider router={router} />
  </QueryClientProvider>,
)
