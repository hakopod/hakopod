import React from 'react'
import { createRoot } from 'react-dom/client'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { createRootRoute, createRouter, Outlet, RouterProvider } from '@tanstack/react-router'
import '../../src/styles.css'

// Browser-only, synthetic test data. This module never calls the original fetch.
const scenario = new URLSearchParams(location.search).get('fixture') || 'ready'
const role = scenario === 'readonly' ? 'reader' : 'administrator'
const cloud = scenario === 'cloud'
const emptyPolicy = {
  enabled: false,
  client_ip_source: 'connection',
  trusted_proxy_cidrs: [],
  client_ip_header: '',
  country_header: '',
  rules: [],
}
const policy = {
  ...emptyPolicy,
  enabled: true,
  rules: [
    {
      id: 'private-api',
      host: 'app.example.test',
      path_prefix: '/api/private',
      allow_cidrs: ['198.51.100.0/24', '2001:db8::/32'],
      deny_cidrs: ['198.51.100.22/32'],
      allow_countries: [],
      deny_countries: [],
      requests_per_second: 12,
    },
    {
      id: 'public-site',
      host: 'app.example.test',
      path_prefix: '/',
      allow_cidrs: [],
      deny_cidrs: [],
      allow_countries: [],
      deny_countries: [],
      requests_per_second: 80,
    },
  ],
}
const identity = {
  id: 'synthetic-edge-review-user',
  name: 'Review Operator',
  email: 'review@example.invalid',
  admin: role === 'administrator',
  owner: false,
  credential_type: 'browser',
  permissions: ['deployments:read', 'deployments:write', 'logs:read'],
  project_roles: [{ project: 'review-project', role: role === 'reader' ? 'viewer' : 'admin' }],
  host_permissions: [],
  avatar_url: '',
  mfa_required: false,
}
const fields = [
  { name: 'maxconn', description: 'Maximum concurrent connections, from 16 to 65536.', example: '1024' },
  { name: 'timeout-client', description: 'Client inactivity timeout, from 1ms to 24h.', example: '30s' },
  { name: 'timeout-server', description: 'Backend inactivity timeout, from 1ms to 24h.', example: '30s' },
  { name: 'load-balance', description: 'Backend balancing algorithm, such as roundrobin or leastconn.', example: 'leastconn' },
  { name: 'max-content-length', description: 'Maximum declared request body size in bytes. Streaming bodies need application limits.', example: '10485760' },
]
const fixture = {
  synthetic: true,
  ready: false,
  scenario,
  saveMode: 'error',
  requests: [] as { method: string; path: string; allowed: boolean; body?: unknown }[],
  blocked: [] as string[],
  patches: [] as any[],
  releaseSave: undefined as undefined | (() => void),
  releaseRead: undefined as undefined | (() => void),
  status: {
    revision: 7,
    drift: scenario === 'drift',
    change: {
      settings: {},
      edge: structuredClone(policy),
      status: scenario === 'queued' ? 'queued' : 'applied',
      error: '',
    },
    observed: {
      namespace: 'synthetic-ingress',
      name: 'synthetic-controller',
      resource_version: 'synthetic-rv-7',
      applied_revision: '7',
      settings: { maxconn: '1024', 'timeout-client': '30s', 'timeout-server': '30s' },
      fields,
      edge: structuredClone(scenario === 'empty' ? emptyPolicy : policy),
    },
  },
  advance() {
    this.status.revision++
    this.status.observed.resource_version = `synthetic-rv-${this.status.revision}`
    this.status.observed.settings['timeout-server'] = '55s'
  },
}
Object.assign(window, { __edgeFixture: fixture })

function failure(message: string, code: string) {
  return { error: { code, message: `Synthetic fixture: ${message}` } }
}

window.fetch = async (input: RequestInfo | URL, init?: RequestInit) => {
  const request = input instanceof Request ? input : new Request(new URL(String(input), location.origin), init)
  const url = new URL(request.url)
  const method = init?.method || request.method
  let data: unknown
  let status = 200
  let body: any
  if (url.origin === location.origin && method === 'GET') {
    switch (url.pathname) {
      case '/api/me':
        data = identity
        break
      case '/api/projects':
        data = { items: [{ id: 'synthetic-project', name: 'review-project', display_name: 'Review project', personal: false, environments: [{ name: 'development' }] }] }
        break
      case '/api/auth/status':
        data = { setup_required: false, deployment_mode: cloud ? 'managed-cloud' : 'self-hosted', password: true, signup_enabled: false, providers: [], passkeys: true, totp: true, email_delivery: false }
        break
      case '/api/license':
        data = { valid: false, plan: 'free', state: 'inactive', installation_id: 'synthetic-installation', revision: 1, edition: 'community', catalog: [] }
        break
      case '/api/alarms':
        data = { items: [], next_cursor: '', summary: { active: 0, unread: 0 } }
        break
      case '/api/auth/security':
        data = { password_enabled: true, totp_enabled: false, recovery_codes_remaining: 0, passkeys: [], mfa_verified: false, organization_mfa_required: false }
        break
      case '/api/auth/sessions':
        data = { items: [] }
        break
      case '/api/settings/haproxy':
        if (role === 'reader' || cloud) {
          status = 403
          data = failure('installation administration is unavailable.', 'forbidden')
        } else if (scenario === 'error') {
          status = 503
          data = failure('HAProxy observations are unavailable.', 'proxy_unavailable')
        } else {
          if (scenario === 'loading') {
            await new Promise<void>((resolve, reject) => {
              fixture.releaseRead = resolve
              request.signal.addEventListener('abort', () => reject(new DOMException('Aborted', 'AbortError')), { once: true })
            })
          }
          data = structuredClone(fixture.status)
        }
        break
    }
  }
  if (url.origin === location.origin && method === 'PATCH' && url.pathname === '/api/settings/haproxy') {
    body = await request.json()
    fixture.patches.push(body)
    if (fixture.saveMode === 'hold') await new Promise<void>((resolve) => { fixture.releaseSave = resolve })
    if (role === 'reader' || cloud) {
      status = 403
      data = failure('installation administration is unavailable.', 'forbidden')
    } else if (body.expected_revision !== fixture.status.revision || body.expected_resource_version !== fixture.status.observed.resource_version) {
      status = 409
      data = failure('configuration changed after review; your draft was not applied.', 'proxy_conflict')
    } else if (fixture.saveMode !== 'success') {
      status = 503
      data = failure('the change could not be saved; your draft was not applied.', 'unavailable')
    } else if ((!body.settings || !Object.keys(body.settings).length) && !body.edge) {
      status = 400
      data = failure('at least one configuration change is required.', 'invalid_proxy')
    } else {
      fixture.status.revision++
      fixture.status.change = { settings: body.settings || {}, edge: body.edge, status: 'queued', error: '' }
      status = 202
      data = { revision: fixture.status.revision, status: 'queued' }
      // Observed state stays unchanged: accepting intent is not runtime success.
    }
  }
  fixture.requests.push({ method, path: url.pathname, allowed: data !== undefined, ...(body === undefined ? {} : { body }) })
  if (fixture.requests.length > 256) fixture.requests.shift()
  if (data === undefined) {
    const blocked = `${method} ${url.origin}${url.pathname}`
    fixture.blocked.push(blocked)
    throw new Error(`Synthetic fixture blocked unexpected request: ${blocked}`)
  }
  return new Response(JSON.stringify(data), { status, headers: { 'Content-Type': 'application/json' } })
}

const [{ DashboardShell }, settings, infrastructure, editor] = await Promise.all([
  import('../../src/components/shell'),
  import('../../src/routes/settings'),
  import('../../src/routes/infrastructure'),
  import('../../src/routes/settings.edge'),
])
const root = createRootRoute({ component: () => <DashboardShell><Outlet /></DashboardShell> })
const routes = [
  [settings.Route, '/settings'],
  [infrastructure.Route, '/infrastructure'],
  [editor.Route, '/settings/edge'],
].map(([route, path]) => (route as typeof settings.Route).update({ id: path, path, getParentRoute: () => root } as never))
const router = createRouter({ routeTree: root.addChildren(routes), defaultPreload: false })
const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false, refetchOnWindowFocus: false, gcTime: 0 } } })
createRoot(document.getElementById('root')!).render(<QueryClientProvider client={queryClient}><RouterProvider router={router} /></QueryClientProvider>)
fixture.ready = true
