// Development-only visual-review entrypoint. It is not imported by product routes
// or the production dashboard build. Run it from a VM with Vite and open:
// /scripts/slack-ui-review.html?scenario=pro-connected&theme=dark
import '../src/styles.css'
import { createRoot } from 'react-dom/client'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import {
  createMemoryHistory,
  Outlet,
  createRootRoute,
  createRoute,
  createRouter,
  RouterProvider,
  useLocation,
  useNavigate,
} from '@tanstack/react-router'
import { SettingsLayout } from '@hakopod/hatch-ui/blocks/settings-layout'
import { PageHeader } from '../src/components/shared'
import { SlackIntegrationPage, SlackSettingsPanel } from '../src/components/slack-settings'
import { ScopeContext } from '../src/lib/scope'

type Scenario =
  | 'pro-connected'
  | 'selfhost-setup'
  | 'cloud-owner'
  | 'cloud-unpaid'
  | 'expired'
  | 'denied'
  | 'error'
  | 'test-pending'
  | 'mutation-error'
  | 'events-success-channel-error'
  | 'paginated-channels'
  | 'channel-page-cap'
  | 'catalog-unavailable'
const query = new URLSearchParams(window.location.search)
const scenario = (query.get('scenario') || 'pro-connected') as Scenario
const theme = query.get('theme') === 'paper' ? 'light' : 'dark'
document.documentElement.dataset.theme = theme
const cloudScenario = scenario === 'cloud-owner' || scenario === 'cloud-unpaid'
document.documentElement.dataset.edition = cloudScenario ? 'cloud' : 'self-hosted'
document.documentElement.className = theme === 'light' ? 'light' : 'dark'

// This mirrors internal/slackevents/catalog.go so the review uses the actual selectable events.
const eventCatalog = [
  {
    id: 'alarm.opened',
    category: 'Alarms',
    label: 'Alarm opened',
    description: 'An alarm incident opens.',
  },
  {
    id: 'alarm.resolved',
    category: 'Alarms',
    label: 'Alarm resolved',
    description: 'An alarm incident resolves.',
  },
  {
    id: 'audit',
    category: 'Audit',
    label: 'Audit event',
    description: 'A recorded Hakopod audit event occurs.',
  },
  {
    id: 'deployment.queued',
    category: 'Deployments',
    label: 'Deployment queued',
    description: 'A deployment is accepted into the durable queue.',
  },
  {
    id: 'deployment.started',
    category: 'Deployments',
    label: 'Deployment started',
    description: 'A worker starts a deployment.',
  },
  {
    id: 'deployment.succeeded',
    category: 'Deployments',
    label: 'Deployment succeeded',
    description: 'A deployment completes successfully.',
  },
  {
    id: 'deployment.failed',
    category: 'Deployments',
    label: 'Deployment failed',
    description: 'A deployment finishes with a failure.',
  },
  {
    id: 'deployment.cancelled',
    category: 'Deployments',
    label: 'Deployment cancelled',
    description: 'A deployment is cancelled.',
  },
  {
    id: 'deployment.superseded',
    category: 'Deployments',
    label: 'Deployment superseded',
    description: 'A newer deployment supersedes this deployment.',
  },
  {
    id: 'deployment.rollback.requested',
    category: 'Deployments',
    label: 'Rollback requested',
    description: 'A rollback deployment is requested.',
  },
  {
    id: 'deployment.cancellation.requested',
    category: 'Deployments',
    label: 'Cancellation requested',
    description: 'Cancellation is requested for a deployment.',
  },
  {
    id: 'application.created',
    category: 'Applications',
    label: 'Application created',
    description: 'A new application is accepted.',
  },
  {
    id: 'application.configuration.updated',
    category: 'Applications',
    label: 'Application configuration accepted',
    description: 'An application configuration change is accepted.',
  },
  {
    id: 'application.renamed',
    category: 'Applications',
    label: 'Application renamed',
    description: 'An application display name changes.',
  },
  {
    id: 'application.deleted',
    category: 'Applications',
    label: 'Application deleted',
    description: 'An empty application is deleted.',
  },
  {
    id: 'service.added',
    category: 'Services',
    label: 'Service added',
    description: 'A service is accepted into an application.',
  },
  {
    id: 'service.removed',
    category: 'Services',
    label: 'Service removed',
    description: 'A service removal is accepted.',
  },
  {
    id: 'service.renamed',
    category: 'Services',
    label: 'Service renamed',
    description: 'A service display name changes.',
  },
  {
    id: 'service.configuration.updated',
    category: 'Services',
    label: 'Service configuration accepted',
    description: 'A service configuration change is accepted.',
  },
  {
    id: 'service.image.updated',
    category: 'Services',
    label: 'Service image accepted',
    description: 'A service image change is accepted.',
  },
  {
    id: 'service.variables.updated',
    category: 'Services',
    label: 'Service variables accepted',
    description: 'A service environment variable or secret reference change is accepted.',
  },
  {
    id: 'service.resources.updated',
    category: 'Services',
    label: 'Service resources accepted',
    description: 'A service resource change is accepted.',
  },
  {
    id: 'service.scale.updated',
    category: 'Services',
    label: 'Service scaling accepted',
    description: 'A service replicas or autoscaling change is accepted.',
  },
  {
    id: 'service.suspended',
    category: 'Services',
    label: 'Service suspension accepted',
    description: 'A service suspension is accepted.',
  },
  {
    id: 'service.resumed',
    category: 'Services',
    label: 'Service resume accepted',
    description: 'A service resume is accepted.',
  },
  {
    id: 'service.restart.requested',
    category: 'Services',
    label: 'Service restart requested',
    description: 'A service restart is requested.',
  },
  {
    id: 'service.network.updated',
    category: 'Services',
    label: 'Service network accepted',
    description: 'A service ports, HTTP, TLS or network access change is accepted.',
  },
  {
    id: 'service.storage.updated',
    category: 'Services',
    label: 'Service storage accepted',
    description: 'A service storage change is accepted.',
  },
  {
    id: 'service.healthcheck.updated',
    category: 'Services',
    label: 'Service health check accepted',
    description: 'A service health check change is accepted.',
  },
  {
    id: 'service.placement.updated',
    category: 'Services',
    label: 'Service placement accepted',
    description: 'A service placement change is accepted.',
  },
  {
    id: 'service.command.updated',
    category: 'Services',
    label: 'Service command accepted',
    description: 'A service command change is accepted.',
  },
  {
    id: 'service.delivery.updated',
    category: 'Services',
    label: 'Service delivery accepted',
    description: 'A service update strategy, serverless or delivery configuration change is accepted.',
  },
  {
    id: 'service.update.started',
    category: 'Services',
    label: 'Service update started',
    description: 'A service update starts.',
  },
  {
    id: 'service.ready',
    category: 'Services',
    label: 'Service ready',
    description: 'A service becomes ready.',
  },
  {
    id: 'service.failed',
    category: 'Services',
    label: 'Service failed',
    description: 'A service update fails.',
  },
  {
    id: 'service.job.scheduled',
    category: 'Services',
    label: 'Job scheduled',
    description: 'A scheduled job configuration is applied.',
  },
  {
    id: 'service.job.completed',
    category: 'Services',
    label: 'Job completed',
    description: 'A service job completes.',
  },
  {
    id: 'service.certificate.renewed',
    category: 'Services',
    label: 'Certificate renewed',
    description: 'A service certificate renewal completes.',
  },
]

const base = {
  mode: 'self_hosted',
  available: true,
  configured: true,
  setup_available: true,
  team: { id: 'T_fixture', name: 'Fixture workspace' },
  channel: { id: 'C_fixture', name: 'platform-alerts', is_private: false },
  events: ['alarm.opened', 'alarm.resolved'],
  event_catalog: eventCatalog,
  revision: 7,
}
const fixtures: Record<
  Scenario,
  {
    status?: number
    integration: Record<string, unknown>
    license: Record<string, unknown>
    deliveries: Record<string, unknown>
  }
> = {
  'pro-connected': {
    integration: base,
    license: { plan: 'pro', catalog: [{ id: 'slack_notifications', enabled: true }] },
    deliveries: {
      items: [
        {
          id: 17,
          event: 'audit',
          status: 'sent',
          attempts: 1,
          last_error: '',
          created_at: '2026-10-04T06:00:00Z',
          finished_at: '2026-10-04T06:00:01Z',
        },
      ],
    },
  },
  'selfhost-setup': {
    integration: {
      ...base,
      configured: false,
      channel: undefined,
      events: [],
      manifest: {
        display_information: { name: 'Hakopod' },
        oauth_config: {
          redirect_urls: ['https://console.example.test/api/integrations/slack/callback'],
        },
        settings: { socket_mode_enabled: false },
      },
    },
    license: { plan: 'pro', catalog: [{ id: 'slack_notifications', enabled: true }] },
    deliveries: { items: [] },
  },
  'cloud-owner': {
    integration: { ...base, mode: 'cloud', configured: false, channel: undefined, events: [] },
    license: { plan: 'pro', catalog: [{ id: 'slack_notifications', enabled: true }] },
    deliveries: { items: [] },
  },
  'cloud-unpaid': {
    integration: {
      ...base,
      mode: 'cloud',
      available: false,
      setup_available: false,
      configured: false,
      channel: undefined,
      events: [],
      reason: 'license_required',
    },
    license: { plan: 'free', catalog: [{ id: 'slack_notifications', enabled: false }] },
    deliveries: { items: [] },
  },
  expired: {
    integration: { ...base, available: false, setup_available: false, reason: 'license_required' },
    license: { plan: 'free', catalog: [{ id: 'slack_notifications', enabled: false }] },
    deliveries: {
      items: [
        {
          id: 18,
          event: 'alarm.opened',
          status: 'failed',
          attempts: 3,
          last_error: 'Fixture delivery failure',
          created_at: '2026-10-04T06:00:00Z',
        },
      ],
    },
  },
  denied: {
    integration: base,
    license: { plan: 'pro', catalog: [{ id: 'slack_notifications', enabled: true }] },
    deliveries: { items: [] },
  },
  error: {
    status: 503,
    integration: base,
    license: { plan: 'pro', catalog: [{ id: 'slack_notifications', enabled: true }] },
    deliveries: { items: [] },
  },
  'test-pending': {
    integration: base,
    license: { plan: 'pro', catalog: [{ id: 'slack_notifications', enabled: true }] },
    deliveries: {
      items: [
        {
          id: 19,
          event: 'test',
          status: 'pending',
          attempts: 0,
          last_error: '',
          created_at: '2026-10-04T06:00:00Z',
        },
      ],
    },
  },
  'mutation-error': {
    integration: {
      ...base,
      events: ['alarm.opened', 'service.update.started', 'legacy.operation.completed'],
    },
    license: { plan: 'pro', catalog: [{ id: 'slack_notifications', enabled: true }] },
    deliveries: { items: [] },
  },
  'events-success-channel-error': {
    integration: base,
    license: { plan: 'pro', catalog: [{ id: 'slack_notifications', enabled: true }] },
    deliveries: { items: [] },
  },
  'paginated-channels': {
    integration: base,
    license: { plan: 'pro', catalog: [{ id: 'slack_notifications', enabled: true }] },
    deliveries: { items: [] },
  },
  'channel-page-cap': {
    integration: base,
    license: { plan: 'pro', catalog: [{ id: 'slack_notifications', enabled: true }] },
    deliveries: { items: [] },
  },
  'catalog-unavailable': {
    integration: { ...base, event_catalog: undefined },
    license: { plan: 'pro', catalog: [{ id: 'slack_notifications', enabled: true }] },
    deliveries: { items: [] },
  },
}
const fixture = fixtures[fixtureKey(scenario)]
let fixtureIntegration = structuredClone(fixture.integration) as Record<string, unknown>
let channelFailuresRemaining = scenario === 'events-success-channel-error' ? 1 : 0

function fixtureKey(value: Scenario): Scenario {
  return value in fixtures ? value : 'pro-connected'
}

async function fixtureRequestBody(input: RequestInfo | URL, init?: RequestInit) {
  if (typeof init?.body === 'string') {
    try {
      return JSON.parse(init.body) as Record<string, unknown>
    } catch {
      return {}
    }
  }
  if (input instanceof Request) {
    try {
      return (await input.clone().json()) as Record<string, unknown>
    } catch {
      return {}
    }
  }
  return {}
}

function fixtureMethod(input: RequestInfo | URL, init?: RequestInit) {
  if (init?.method) return init.method.toUpperCase()
  if (input instanceof Request) return input.method.toUpperCase()
  return 'GET'
}

function fixtureRequestURL(input: RequestInfo | URL) {
  return new URL(
    typeof input === 'string' ? input : input instanceof URL ? input.href : input.url,
    window.location.origin,
  )
}

function fixtureRevisionConflict() {
  return Response.json(
    {
      error: {
        code: 'revision_conflict',
        message: 'The Slack configuration changed. Review the latest revision before saving.',
      },
    },
    { status: 409 },
  )
}

const originalFetch = window.fetch.bind(window)
window.fetch = async (input, init) => {
  const url = fixtureRequestURL(input)
  if (url.pathname === '/cloud-api/session')
    return Response.json({
      user: {
        id: 'fixture-cloud-owner',
        email: 'owner@example.test',
        name: 'Fixture workspace owner',
      },
      workspace_id: 'workspace-fixture',
      plan: {
        id: cloudScenario ? (scenario === 'cloud-unpaid' ? 'free' : 'pro') : 'pro',
        workspaces: 1,
        members: 5,
        nodes: 1,
        included_compute: true,
      },
    })
  if (url.pathname === '/cloud-api/workspaces')
    return Response.json([
      {
        id: 'workspace-fixture',
        name: 'Fixture workspace',
        plan: scenario === 'cloud-unpaid' ? 'free' : 'pro',
        revision: 1,
        members: { 'fixture-cloud-owner': { email: 'owner@example.test', role: 'owner' } },
      },
    ])
  if (!url.pathname.startsWith('/api/')) return originalFetch(input, init)
  const method = fixtureMethod(input, init)
  if (scenario === 'error' && url.pathname === '/api/integrations/slack')
    return Response.json(
      { error: { code: 'fixture_unavailable', message: 'Fixture API unavailable.' } },
      { status: fixture.status || 503 },
    )
  if (url.pathname === '/api/license') return Response.json(fixture.license)
  if (url.pathname === '/api/integrations/slack/deliveries')
    return Response.json(fixture.deliveries)
  if (url.pathname === '/api/integrations/slack/channels' && scenario === 'paginated-channels') {
    const before = url.searchParams.get('before')
    if (!before) return Response.json({ items: [], next_before: 'fixture-page-1' })
    if (before === 'fixture-page-1')
      return Response.json({
        items: [{ id: 'C_builds', name: 'builds', is_private: false }],
        next_before: 'fixture-page-2',
      })
    if (before === 'fixture-page-2')
      return Response.json({
        items: [{ id: 'C_private', name: 'security', is_private: true }],
      })
    return Response.json({ items: [] })
  }
  if (url.pathname === '/api/integrations/slack/channels' && scenario === 'channel-page-cap') {
    const before = url.searchParams.get('before')
    const page = before ? Number(before.replace('fixture-page-', '')) : 0
    return Response.json({
      items: [{ id: `C_fixture_${page}`, name: `channel-${page}`, is_private: false }],
      next_before: `fixture-page-${page + 1}`,
    })
  }
  if (url.pathname === '/api/integrations/slack/channels')
    return Response.json({
      items: [
        { id: 'C_fixture', name: 'platform-alerts', is_private: false },
        { id: 'C_private', name: 'security', is_private: true },
      ],
    })
  if (url.pathname === '/api/integrations/slack/test' && method === 'POST')
    return Response.json({ id: 'fixture-test-19', status: 'pending' }, { status: 202 })
  if (url.pathname === '/api/integrations/slack/connect' && method === 'POST')
    return Response.json({ authorization_url: 'https://slack.com/oauth/v2/authorize?fixture=1' })
  if (url.pathname === '/api/integrations/slack' && method === 'DELETE') {
    if (scenario === 'mutation-error')
      return Response.json(
        {
          error: {
            code: 'revision_conflict',
            message: 'The Slack connection changed. Refresh before disconnecting.',
          },
        },
        { status: 409 },
      )
    return Response.json({ deleted: true })
  }
  if (url.pathname === '/api/integrations/slack' && method === 'GET')
    return Response.json(fixtureIntegration)
  if (url.pathname === '/api/integrations/slack/events' && method === 'PUT') {
    if (scenario === 'mutation-error') return fixtureRevisionConflict()
    const body = await fixtureRequestBody(input, init)
    if (body.expected_revision !== fixtureIntegration.revision) return fixtureRevisionConflict()
    fixtureIntegration = {
      ...fixtureIntegration,
      events: Array.isArray(body.events) ? body.events : fixtureIntegration.events,
      revision: Number(fixtureIntegration.revision) + 1,
    }
    return Response.json(fixtureIntegration)
  }
  if (url.pathname === '/api/integrations/slack/channel' && method === 'PUT') {
    if (scenario === 'mutation-error') return fixtureRevisionConflict()
    if (channelFailuresRemaining > 0) {
      channelFailuresRemaining -= 1
      return Response.json(
        {
          error: {
            code: 'fixture_channel_unavailable',
            message: 'Fixture channel update is temporarily unavailable.',
          },
        },
        { status: 503 },
      )
    }
    const body = await fixtureRequestBody(input, init)
    if (body.expected_revision !== fixtureIntegration.revision) return fixtureRevisionConflict()
    const channel =
      body.channel_id === 'C_private'
        ? { id: 'C_private', name: 'security', is_private: true }
        : { id: 'C_fixture', name: 'platform-alerts', is_private: false }
    fixtureIntegration = {
      ...fixtureIntegration,
      channel,
      revision: Number(fixtureIntegration.revision) + 1,
    }
    return Response.json(fixtureIntegration)
  }
  return Response.json(
    {
      error: {
        code: 'fixture_unknown_request',
        message: `Unmapped fixture request: ${method} ${url.pathname}`,
      },
    },
    { status: 500 },
  )
}

function FixtureSettings() {
  const location = useLocation()
  const navigate = useNavigate()
  return (
    <div className="hako-shell settings-page">
      <div className="fixture-banner" role="status">
        UI fixture · artificial data · no Slack, API, account, or workload is contacted
      </div>
      <div className="hako-page-content">
        {location.pathname === '/settings' ? (
          <>
            <PageHeader title="Settings" />
            <SettingsLayout
              sections={[
                { id: 'account', label: 'Account security', group: 'Personal' },
                { id: 'integrations', label: 'Integrations', group: 'Installation' },
              ]}
              active="account"
              onSectionChange={(section) => {
                if (section === 'integrations') void navigate({ to: '/settings/integrations' })
              }}
            >
              <p className="m-0 text-sm">Choose Integrations to manage Slack notifications.</p>
            </SettingsLayout>
          </>
        ) : (
          <Outlet />
        )}
      </div>
    </div>
  )
}
function FixtureDetail() {
  return <SlackIntegrationPage />
}
function FixtureIntegrations() {
  const location = useLocation()
  if (location.pathname === '/settings/integrations/slack') return <Outlet />
  return (
    <div className="grid gap-4">
      <PageHeader title="Integrations" />
      <SlackSettingsPanel />
    </div>
  )
}
const rootRoute = createRootRoute({ component: () => <Outlet /> })
const settingsRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: '/settings',
  component: FixtureSettings,
})
const integrationsRoute = createRoute({
  getParentRoute: () => settingsRoute,
  path: 'integrations',
  component: FixtureIntegrations,
})
const detailRoute = createRoute({
  getParentRoute: () => integrationsRoute,
  path: 'slack',
  validateSearch: (search: Record<string, unknown>) => ({
    configure:
      search.configure === '1' || search.configure === 1 || search.configure === true
        ? true
        : undefined,
    connected:
      search.connected === '1' || search.connected === 1 || search.connected === true
        ? true
        : undefined,
  }),
  component: FixtureDetail,
})
const routeTree = rootRoute.addChildren([
  settingsRoute.addChildren([integrationsRoute.addChildren([detailRoute])]),
])
const router = createRouter({
  routeTree,
  history: createMemoryHistory({
    initialEntries: [
      query.get('view') === 'settings'
        ? '/settings'
        : query.get('view') === 'detail'
          ? `/settings/integrations/slack${
              query.get('configure') === '1' ||
              scenario === 'mutation-error' ||
              scenario === 'events-success-channel-error' ||
              scenario === 'paginated-channels' ||
              scenario === 'channel-page-cap' ||
              scenario === 'catalog-unavailable'
                ? '?configure=1'
                : ''
            }`
          : '/settings/integrations',
    ],
  }),
})
const cache = new QueryClient({
  defaultOptions: { queries: { retry: false, staleTime: Infinity } },
})
const admin = scenario !== 'denied' && !cloudScenario
const scope = {
  project: '',
  environment: '',
  identity: {
    id: cloudScenario ? 'fixture-cloud-owner' : 'fixture-admin',
    name: cloudScenario ? 'Fixture workspace owner' : 'Fixture administrator',
    email: cloudScenario ? 'owner@example.test' : 'fixture@example.test',
    admin,
    credential_type: 'browser',
    permissions: [],
    project_roles: [],
  },
  can: () => admin,
  syncScope: () => {},
}

createRoot(document.getElementById('root')!).render(
  <QueryClientProvider client={cache}>
    <ScopeContext.Provider value={scope as any}>
      <RouterProvider router={router} />
    </ScopeContext.Provider>
  </QueryClientProvider>,
)
