import React from 'react'
import { createRoot } from 'react-dom/client'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { createRootRoute, createRoute, createRouter, Outlet, RouterProvider } from '@tanstack/react-router'
import '../../src/styles.css'

// Browser-only, synthetic data. This module never calls the original fetch.
const scenario = new URLSearchParams(location.search).get('fixture') || 'ready'
const applicationID = 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa'
const deploymentID = 'dddddddddddddddddddddddddddddddd'
const applicationPath = `/api/applications/${applicationID}`
const servicePath = `${applicationPath}/services/api`
const scopedIssuersPath = `${applicationPath}/tls/issuers`
const operator = scenario === 'operator'
const readonly = scenario === 'readonly'
const cloud = !operator
const now = new Date().toISOString()
const application = {
  id: applicationID,
  name: 'tls-review',
  display_name: 'TLS review application',
  project: 'review-project',
  environment: 'development',
  revision: 7,
  status: 'unknown',
  spec: {
    schema_version: 1,
    name: 'tls-review',
    services: {
      api: {
        image: `registry.example.invalid/synthetic-api@sha256:${'1'.repeat(64)}`,
        public: true,
        port: 8080,
        replicas: 1,
        resources: { cpu: '100m', memory: '128Mi' },
      },
    },
    domains: {},
    networks: {},
  },
  observed: {},
  created_at: now,
  updated_at: now,
  deployments: [],
}
const identity = {
  id: 'synthetic-tls-review-user',
  name: 'Review Developer',
  email: 'review@example.invalid',
  admin: operator,
  owner: false,
  project: '',
  environment: '',
  credential_type: 'browser',
  permissions: readonly ? ['deployments:read', 'logs:read'] : ['deployments:read', 'deployments:write', 'logs:read'],
  project_roles: [{ project: 'review-project', role: readonly ? 'viewer' : 'developer' }],
  host_permissions: [],
  avatar_url: '',
  mfa_required: false,
}
const defaultIssuer = {
  kind: 'ClusterIssuer',
  default: true,
  name: 'shared-acme',
  email: 'operator@example.invalid',
  server: 'https://acme-v02.api.letsencrypt.org/directory',
  ready: true,
  conditions: [{ type: 'Ready', status: 'True', reason: 'SyntheticFixture', message: 'Synthetic default issuer observation.' }],
}
const ownIssuer = {
  kind: 'Issuer',
  default: false,
  name: 'shared-acme',
  email: 'developer@example.invalid',
  server: 'https://acme-staging-v02.api.letsencrypt.org/directory',
  ready: true,
  conditions: [{ type: 'Ready', status: 'True', reason: 'SyntheticFixture', message: 'Synthetic application issuer observation.' }],
}
const fixture = {
  synthetic: true,
  ready: false,
  scenario,
  applicationID,
  deploymentID,
  scopedIssuersPath,
  createMode: 'error',
  tlsMode: 'error',
  requests: [] as { method: string; path: string; allowed: boolean }[],
  blocked: [] as string[],
  creates: [] as { path: string; body: any }[],
  submissions: [] as { body: any; key: string | null }[],
  releaseCreate: undefined as undefined | (() => void),
  releaseTLS: undefined as undefined | (() => void),
  releaseIssuers: undefined as undefined | (() => void),
  issuers: {
    installed: scenario !== 'no-cert-manager',
    items: scenario === 'no-cert-manager' ? [] : scenario === 'missing-default' ? [structuredClone(ownIssuer)] : [structuredClone(defaultIssuer), structuredClone(ownIssuer)],
    message: scenario === 'no-cert-manager'
      ? 'Synthetic fixture: cert-manager is not installed. PEM uploads remain available.'
      : scenario === 'missing-default'
        ? 'Synthetic fixture: the installation default issuer is unavailable. Use an application issuer or upload a certificate.'
        : '',
  },
  tls: {
    hostname: 'api.tls-review.example.invalid',
    enabled: scenario === 'existing-issuer' || scenario === 'existing-upload',
    ready: false,
    source: scenario === 'existing-issuer' ? 'cert-manager' : scenario === 'existing-upload' ? 'uploaded' : 'none',
    ...(scenario === 'existing-issuer' ? { issuer: 'shared-acme', issuer_kind: 'Issuer' } : {}),
    message: 'Synthetic fixture: no live certificate readiness has been checked.',
  },
}
Object.assign(window, { __tlsFixture: fixture })

function failure(message: string, code: string) {
  return { error: { code, message: `Synthetic fixture: ${message}` } }
}

window.fetch = async (input: RequestInfo | URL, init?: RequestInit) => {
  const request = input instanceof Request ? input : new Request(new URL(String(input), location.origin), init)
  const url = new URL(request.url)
  const method = init?.method || request.method
  let data: unknown
  let status = 200
  if (url.origin === location.origin && method === 'GET') {
    switch (url.pathname) {
      case '/api/me': data = identity; break
      case '/api/projects':
        data = { items: [{ id: 'synthetic-project', name: 'review-project', display_name: 'Review project', personal: false, environments: [{ name: 'development' }] }] }
        break
      case '/api/auth/status':
        data = { setup_required: false, deployment_mode: cloud ? 'managed-cloud' : 'self-hosted', password: true, signup_enabled: false, providers: [], passkeys: true, totp: true, email_delivery: false }
        break
      case '/api/license':
        data = { valid: false, plan: 'free', state: 'inactive', installation_id: 'synthetic-installation', revision: 1, edition: 'community', catalog: [] }
        break
      case '/api/alarms': data = { items: [], next_cursor: '', summary: { active: 0, unread: 0 } }; break
      case applicationPath: data = application; break
      case `${servicePath}/tls`: data = fixture.tls; break
      case `${servicePath}/delivery`:
        data = { public_tcp: [], observed_at: now, public_tcp_policy: { allowed: true, message: '' } }
        break
      case `${servicePath}/certificates`: data = { items: [] }; break
      case `/api/deployments/${deploymentID}`:
        data = { id: deploymentID, application_id: applicationID, revision: 8, status: 'queued', spec: application.spec, result: {}, error: '', created_at: now, events: [] }
        break
      case '/api/tls/issuers':
        data = { ...fixture.issuers, items: fixture.issuers.items.filter((item) => cloud ? item.default : item.kind === 'ClusterIssuer') }
        break
      case scopedIssuersPath:
        if (scenario === 'forbidden') {
          status = 403
          data = failure('certificate issuer access was denied.', 'forbidden')
        } else {
          if (scenario === 'loading') {
            await new Promise<void>((resolve, reject) => {
              fixture.releaseIssuers = resolve
              request.signal.addEventListener('abort', () => reject(new DOMException('Aborted', 'AbortError')), { once: true })
            })
          }
          data = structuredClone(fixture.issuers)
        }
        break
    }
  }
  if (url.origin === location.origin && method === 'POST' && [scopedIssuersPath, '/api/tls/issuers'].includes(url.pathname)) {
    const body = await request.json()
    fixture.creates.push({ path: url.pathname, body })
    if (fixture.createMode === 'hold') await new Promise<void>((resolve) => { fixture.releaseCreate = resolve })
    if (readonly || (cloud && url.pathname === '/api/tls/issuers') || fixture.createMode === 'forbidden') {
      status = 403
      data = failure('creating this certificate issuer was denied.', 'forbidden')
    } else if (fixture.createMode !== 'success') {
      status = 503
      data = failure('the issuer could not be created. Your draft was not applied.', 'unavailable')
    } else {
      const issuer = {
        kind: url.pathname === scopedIssuersPath ? 'Issuer' : 'ClusterIssuer',
        default: false,
        name: body.name,
        email: body.email,
        server: body.production ? defaultIssuer.server : ownIssuer.server,
        ready: false,
        conditions: [{ type: 'Ready', status: 'Unknown', reason: 'SyntheticFixture', message: 'Synthetic creation response; readiness has not been observed.' }],
      }
      fixture.issuers.items.push(issuer)
      status = 201
      data = issuer
    }
  }
  if (url.origin === location.origin && method === 'POST' && url.pathname === `${servicePath}/tls`) {
    const body = await request.json()
    fixture.submissions.push({ body, key: request.headers.get('Idempotency-Key') })
    if (fixture.tlsMode === 'hold') await new Promise<void>((resolve) => { fixture.releaseTLS = resolve })
    if (readonly) {
      status = 403
      data = failure('TLS deployment access was denied.', 'forbidden')
    } else if (body.expected_revision !== application.revision) {
      status = 409
      data = failure('the application revision changed. Your draft was not applied.', 'conflict')
    } else if (body.issuer && (!['Issuer', 'ClusterIssuer'].includes(body.issuer_kind) || !fixture.issuers.items.some((item) => item.name === body.issuer && item.kind === body.issuer_kind))) {
      status = 400
      data = failure('choose an issuer from this application.', 'invalid_issuer')
    } else if (fixture.tlsMode !== 'success') {
      status = 503
      data = failure('the TLS change could not be submitted. Your draft was not applied.', 'unavailable')
    } else {
      status = 202
      data = { id: deploymentID, application_id: applicationID, revision: 8, status: 'queued', spec: application.spec, result: {}, error: '', created_at: now, events: [] }
      // An accepted deployment never updates certificate readiness in this fixture.
    }
  }
  fixture.requests.push({ method, path: url.pathname, allowed: data !== undefined })
  if (fixture.requests.length > 256) fixture.requests.shift()
  if (data === undefined) {
    const blocked = `${method} ${url.origin}${url.pathname}`
    fixture.blocked.push(blocked)
    throw new Error(`Synthetic fixture blocked unexpected request: ${blocked}`)
  }
  return new Response(JSON.stringify(data), { status, headers: { 'Content-Type': 'application/json' } })
}

const [{ DashboardShell }, applicationModule, infrastructure, { PageHeader, Note }] = await Promise.all([
  import('../../src/components/shell'),
  import('../../src/routes/applications.$applicationId'),
  import('../../src/routes/infrastructure'),
  import('../../src/components/shared'),
])
const root = createRootRoute({ component: () => <DashboardShell><Outlet /></DashboardShell> })
const routes = [
  [applicationModule.Route, '/applications/$applicationId'],
  [infrastructure.Route, '/infrastructure'],
].map(([route, path]) => (route as typeof applicationModule.Route).update({ id: path, path, getParentRoute: () => root } as never))
const accepted = createRoute({
  getParentRoute: () => root,
  path: '/deployments/$deploymentId',
  component: () => <><PageHeader title="Synthetic deployment queued" /><Note>This review ends at accepted intent. It does not simulate a running deployment or certificate readiness.</Note></>,
})
const router = createRouter({ routeTree: root.addChildren([...routes, accepted]), defaultPreload: false })
const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false, refetchOnWindowFocus: false, gcTime: 0 } } })
createRoot(document.getElementById('root')!).render(<QueryClientProvider client={queryClient}><RouterProvider router={router} /></QueryClientProvider>)
fixture.ready = true
