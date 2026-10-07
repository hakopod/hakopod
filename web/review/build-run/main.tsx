import React from 'react'
import { createRoot } from 'react-dom/client'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { createRootRoute, createRouter, Outlet, RouterProvider } from '@tanstack/react-router'
import '../../src/styles.css'

// DEVELOPMENT ONLY. Product routes render artificial responses. All other fetches fail.
const phases = ['queued', 'processing', 'blocked', 'deployed', 'manual'] as const
type Phase = typeof phases[number]
const selected = new URLSearchParams(location.search).get('fixture')
let phase: Phase = phases.includes(selected as Phase) ? selected as Phase : 'queued'
const buildID = 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa'
const runID = 'bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb'
const deploymentID = 'cccccccccccccccccccccccccccccccc'
const applicationID = 'dddddddddddddddddddddddddddddddd'
const now = new Date().toISOString()
const image = `registry.example.invalid/review/web@sha256:${'f'.repeat(64)}`
const permissions = ['deployments:read', 'logs:read']
const identity = {
  id: 'artificial-build-reviewer', name: 'UI reviewer', email: 'review@example.invalid',
  admin: false, owner: false, project: '', environment: '', credential_type: 'browser',
  permissions, project_roles: [{ project: 'review-project', role: 'viewer', permissions }],
  host_permissions: [], mfa_required: false,
}
const spec = {
  schema_version: 1, name: 'first-deployment-review',
  services: { web: { image, port: 3000, public: false, size: 'small', replicas: 1 } },
}
const build = {
  id: buildID, project: 'review-project', environment: 'development',
  name: spec.name, service: 'web', application_id: applicationID,
  provider: 'github', connection_id: 'artificial-git', repository: 'example/review', branch: 'main',
  revision: 1, installed_revision: 1, mode: 'dockerfile', dockerfile: 'Dockerfile',
  context_path: '.', architecture: 'amd64', auto_build: true, auto_deploy: true,
  managed_registry: true, registry_credential: '', reuse_services: [], build_args: {}, build_secrets: {},
  created_at: now, updated_at: now,
}
function observedRun() {
  return {
    id: runID, build_id: buildID, config_revision: 1,
    commit_sha: 'e'.repeat(40), provider: 'github', remote_run_id: 1, github_run_id: 1,
    status: 'completed', conclusion: 'success', image, run_url: '',
    automatic: phase !== 'manual', auto_status: phase === 'manual' ? '' : phase,
    deployment_id: phase === 'deployed' ? deploymentID : '',
    message: phase === 'blocked'
      ? 'Artificial observation: automatic deployment permission was revoked. No deployment was accepted.'
      : phase === 'deployed'
        ? 'Artificial observation: deployment accepted. Inspect the current runtime separately.'
        : 'Artificial observation: the build image is verified.',
    created_at: now, updated_at: now,
  }
}
const deployment = {
  id: deploymentID, application_id: applicationID, revision: 1,
  status: 'succeeded', spec, resolved_spec: spec, events: [], result: { services: [] },
  created_at: now, started_at: now, finished_at: now,
}
const application = {
  id: applicationID, project: build.project, environment: build.environment,
  name: spec.name, revision: 1, spec, status: 'succeeded', created_at: now, updated_at: now,
  deployments: [deployment],
  observed: {
    observed_at: now, status: 'failed',
    services: [{ name: 'web', status: 'failed', ready: 0, desired: 1, message: 'Artificial CrashLoopBackOff observation after the recorded deployment completed.' }],
  },
}
const fixture = { ready: false, synthetic: true, phase, runRequests: 0, requests: [] as unknown[], blocked: [] as string[] }
Object.assign(window, { __buildRunFixture: fixture })
function showState() {
  fixture.phase = phase
  document.getElementById('fixture-state')!.textContent = `Response phase: ${phase}. Run requests: ${fixture.runRequests}.`
}
document.getElementById('advance-fixture')!.addEventListener('click', () => {
  if (phase === 'queued') phase = 'processing'
  else if (phase === 'processing') phase = 'deployed'
  showState()
  // Do not invalidate the query. The product's ten-second polling must observe this change.
})
showState()
function reportError(message: string) {
  document.getElementById('fixture-state')!.textContent = `Fixture error: ${message}`
}
window.addEventListener('error', (event) => reportError(event.message))
window.addEventListener('unhandledrejection', (event) => reportError(String(event.reason)))
window.fetch = async (input: RequestInfo | URL, init?: RequestInit) => {
  const request = input instanceof Request ? input : new Request(new URL(String(input), location.origin), init)
  const url = new URL(request.url)
  const method = init?.method || request.method
  let data: unknown
  if (url.origin === location.origin && method === 'GET') {
    if (url.pathname === '/api/me') data = identity
    else if (url.pathname === '/api/projects') data = { items: [{ id: 'artificial-project', name: build.project, display_name: 'Review project', personal: false, environments: [{ name: build.environment }] }] }
    else if (url.pathname === '/api/auth/status') data = { setup_required: false, deployment_mode: 'self-hosted', password: true, signup_enabled: false, providers: [], passkeys: true, totp: true, email_delivery: false }
    else if (url.pathname === '/api/license') data = { valid: false, plan: 'free', state: 'inactive', installation_id: 'artificial-review-installation', revision: 1, edition: 'community', catalog: [] }
    else if (url.pathname === '/api/alarms') data = { items: [], next_cursor: '', summary: { active: 0, unread: 0 } }
    else if (url.pathname === `/api/builds/${buildID}`) data = build
    else if (url.pathname === `/api/builds/${buildID}/runs`) data = { items: [observedRun()] }
    else if (url.pathname === `/api/builds/${buildID}/runs/${runID}`) {
      fixture.runRequests++
      showState()
      data = observedRun()
    }
    else if (url.pathname === `/api/deployments/${deploymentID}`) data = deployment
    else if (url.pathname === `/api/applications/${applicationID}`) data = application
  }
  if (data === undefined) {
    const blocked = `${method} ${url.origin}${url.pathname}`
    fixture.blocked.push(blocked)
    if (fixture.blocked.length > 128) fixture.blocked.shift()
    throw new Error(`DEVELOPMENT ONLY fixture blocked unexpected request: ${blocked}`)
  }
  fixture.requests.push({ method, path: url.pathname })
  if (fixture.requests.length > 128) fixture.requests.shift()
  return new Response(JSON.stringify(data), { status: 200, headers: { 'Content-Type': 'application/json' } })
}
const [{ DashboardShell }, buildModule, deploymentModule] = await Promise.all([
  import('../../src/components/shell'),
  import('../../src/routes/builds.$buildId'),
  import('../../src/routes/deployments.$deploymentId'),
])
const root = createRootRoute({ component: () => <DashboardShell><Outlet /></DashboardShell> })
const buildRoute = buildModule.Route.update({ id: '/builds/$buildId', path: '/builds/$buildId', getParentRoute: () => root } as never)
const deploymentRoute = deploymentModule.Route.update({ id: '/deployments/$deploymentId', path: '/deployments/$deploymentId', getParentRoute: () => root } as never)
const router = createRouter({ routeTree: root.addChildren([buildRoute, deploymentRoute]), defaultPreload: false })
const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false, refetchOnWindowFocus: false, gcTime: 0 } } })
createRoot(document.getElementById('root')!).render(<QueryClientProvider client={queryClient}><RouterProvider router={router} /></QueryClientProvider>)
fixture.ready = true
