import React from 'react'
import { createRoot } from 'react-dom/client'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { createRootRoute, createRouter, Outlet, RouterProvider } from '@tanstack/react-router'
import '../../src/styles.css'

// Browser-only, synthetic test data for Settings → Network access. This module
// never calls the original fetch.
const scenario = new URLSearchParams(location.search).get('fixture') || 'off'
const policy = (enabled: boolean, firewall = false) => ({
  enabled,
  client_ip_source: 'connection',
  trusted_proxy_cidrs: [] as string[],
  client_ip_header: '',
  allow_cidrs: enabled ? ['10.8.0.0/24', '203.0.113.0/24'] : [],
  firewall,
})
const access = {
  revision: scenario === 'off' || scenario === 'unlicensed' ? 0 : 3,
  policy: policy(scenario === 'pending' || scenario === 'restricted', scenario === 'restricted'),
  client_ip: '203.0.113.20',
  licensed: scenario !== 'unlicensed',
  ...(scenario === 'pending' ? { pending_until: new Date(Date.now() + 4 * 60_000).toISOString() } : {}),
}
const identity = {
  id: 'synthetic-access-review-owner',
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
const fixture = { synthetic: true, ready: false, scenario, requests: [] as string[], unknown: [] as string[] }
Object.assign(window, { __accessFixture: fixture })

const answers: Record<string, unknown> = {
  '/api/me': identity,
  '/api/projects': { items: [{ id: 'synthetic-project', name: 'review-project', display_name: 'Review project', personal: false, environments: [{ name: 'development' }] }] },
  '/api/auth/status': { setup_required: false, deployment_mode: 'self-hosted', password: true, signup_enabled: false, providers: [], passkeys: true, totp: true, email_delivery: false },
  '/api/license': { valid: true, plan: 'pro', state: 'active', installation_id: 'synthetic-installation', revision: 1, edition: 'pro', catalog: [{ id: 'private_access', name: 'Private management access', plan: 'pro', description: '', enabled: scenario !== 'unlicensed' }] },
  '/api/security/advisories': { feed: { feed_enabled: true, osv_enabled: true, revision: 0, issued_at: null, expires_at: null, checked_at: null, stale: false, error: '' }, applications: [], summary: { affected_applications: 0, critical: 0, high: 0 } },
  '/api/alarms': { items: [], next_cursor: '', summary: { active: 0, unread: 0 } },
  '/api/installation/access': access,
}

window.fetch = async (input: RequestInfo | URL, init?: RequestInit) => {
  const request = input instanceof Request ? input : new Request(new URL(String(input), location.origin), init)
  const url = new URL(request.url)
  const method = init?.method || request.method
  fixture.requests.push(`${method} ${url.pathname}`)
  if (url.origin !== location.origin) throw new Error(`Synthetic fixture blocked ${url.origin}`)
  if (method === 'GET' && url.pathname in answers)
    return Response.json(answers[url.pathname])
  if (method !== 'GET' && url.pathname.startsWith('/api/installation/access'))
    return Response.json({ ...access, revision: access.revision + 1, pending_until: new Date(Date.now() + 5 * 60_000).toISOString() })
  fixture.unknown.push(`${method} ${url.pathname}`)
  return Response.json({ error: { code: 'not_found', message: 'Synthetic fixture: not part of this review.' } }, { status: 404 })
}

const [{ DashboardShell }, settings] = await Promise.all([
  import('../../src/components/shell'),
  import('../../src/routes/settings'),
])
const root = createRootRoute({ component: () => <DashboardShell><Outlet /></DashboardShell> })
const routes = [settings.Route.update({ id: '/settings', path: '/settings', getParentRoute: () => root } as never)]
const router = createRouter({ routeTree: root.addChildren(routes), defaultPreload: false })
const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false, refetchOnWindowFocus: false, gcTime: 0 } } })
createRoot(document.getElementById('root')!).render(<QueryClientProvider client={queryClient}><RouterProvider router={router} /></QueryClientProvider>)
fixture.ready = true
