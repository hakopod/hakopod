import React from 'react'
import { createRoot } from 'react-dom/client'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { createRootRoute, createRouter, Outlet, RouterProvider } from '@tanstack/react-router'
import '../../src/styles.css'

// Browser-only, synthetic test data for Settings → Agent policies and
// Approvals. This module never calls the original fetch.
// ?fixture=full (default) | empty | unlicensed | error
const scenario = new URLSearchParams(location.search).get('fixture') || 'full'
const licensed = scenario !== 'unlicensed'
const at = (minutesAgo: number) => new Date(Date.now() - minutesAgo * 60_000).toISOString()
const later = (minutes: number) => new Date(Date.now() + minutes * 60_000).toISOString()
const day = 24 * 60

type Cond = { field: string; operator: string; values: string[] }
const doc = (description: string, when: Cond[], then: Record<string, unknown>, incident = false) => ({
  schema_version: 1,
  description,
  when,
  then,
  ...(incident ? { except: { active_incident: true } } : {}),
})
const approval = (ttl = 900) => ({ effect: 'require_approval', approvers: [{ kind: 'project_admins' }], approval_ttl_seconds: ttl })
const block = { effect: 'block' }

const deploys = doc(
  'Deploys and rollbacks to production wait for the security on-call team or a project administrator.',
  [
    { field: 'team', operator: 'is_any_of', values: ['team-platform', 'team-agents'] },
    { field: 'category', operator: 'is_any_of', values: ['deploy'] },
    { field: 'operation', operator: 'matches', values: ['deploy*', 'rollbackApplication'] },
    { field: 'environment', operator: 'is_any_of', values: ['production'] },
  ],
  {
    effect: 'require_approval',
    approvers: [{ kind: 'team', id: 'team-security' }, { kind: 'project_admins' }],
    approval_ttl_seconds: 900,
    max_credential_ttl_seconds: 900,
  },
  true,
)
const deploysDraft = { ...deploys, description: `${deploys.description} Draft: adds staging.` }

const policy = (
  id: string, name: string, status: 'draft' | 'enforced' | 'monitor', version: number,
  document: ReturnType<typeof doc>, minutesAgo: number, by: string, draft: ReturnType<typeof doc> | null = null,
) => ({
  id, name, status, current_version: version, revision: version + (draft ? 1 : 0),
  document: status === 'draft' ? null : document,
  draft: status === 'draft' ? document : draft,
  created_at: at(minutesAgo + 30 * day), updated_at: at(minutesAgo),
  updated_by: `user-${by.split(' ')[0].toLowerCase()}`, updated_by_name: by,
})
const policies = [
  policy('pol-deploys', 'Production deploys need approval', 'enforced', 7, deploys, 120, 'Maya Chen', deploysDraft),
  policy('pol-pii', 'Block bulk PII export', 'enforced', 3, doc('Stops agents from exporting backups of customer databases.', [{ field: 'category', operator: 'is_any_of', values: ['backup'] }, { field: 'operation', operator: 'matches', values: ['downloadBackup', 'exportBackup*'] }], block), 3 * day, 'Daniel Okafor'),
  policy('pol-readonly', 'Read-only by default', 'monitor', 2, doc('Records every write made by agent keys before this is enforced.', [{ field: 'category', operator: 'is_none_of', values: ['read'] }], block), 5 * day, 'Maya Chen'),
  policy('pol-credentials', 'Time-box write credentials', 'enforced', 4, doc('Keys created by agents expire after one hour.', [{ field: 'category', operator: 'is_any_of', values: ['credentials'] }], { ...approval(3600), max_credential_ttl_seconds: 3600 }), 8 * day, 'Priya Raman'),
  policy('pol-email', 'Outbound email review', 'monitor', 1, doc('Changes to mail delivery settings wait for a project administrator.', [{ field: 'operation', operator: 'matches', values: ['updateEmail*', 'sendTestEmail'] }], approval()), 12 * day, 'Daniel Okafor'),
  policy('pol-eu', 'Finance data stays in EU', 'enforced', 2, doc('Finance project deploys only to EU environments.', [{ field: 'project', operator: 'is_any_of', values: ['proj-finance'] }, { field: 'environment', operator: 'is_none_of', values: ['eu-production', 'eu-staging'] }], block), 20 * day, 'Priya Raman'),
  policy('pol-secrets', 'Secrets never leave vault', 'enforced', 5, doc('Agents cannot read environment secret values.', [{ field: 'operation', operator: 'matches', values: ['getSecret*', 'revealEnvironmentVariable'] }], block), 26 * day, 'Maya Chen'),
  policy('pol-freeze', 'Weekend change freeze', 'draft', 0, doc('Holds production writes from Friday evening to Monday morning.', [{ field: 'environment', operator: 'is_any_of', values: ['production'] }, { field: 'category', operator: 'is_any_of', values: ['write', 'deploy'] }], approval()), 40, 'Maya Chen'),
]
const items = scenario === 'empty' || !licensed ? [] : policies
const list = {
  items,
  summary: {
    total: items.length,
    enforced: items.filter((p) => p.status === 'enforced').length,
    monitor: items.filter((p) => p.status === 'monitor').length,
    draft: items.filter((p) => p.status === 'draft' || p.draft).length,
    pending_approvals: items.length ? 3 : 0,
  },
  licensed,
  evaluating_since: licensed ? at(30 * day) : null,
}
const versions = {
  items: [7, 6, 5].map((version, i) => ({
    version, name: 'Production deploys need approval', status: 'enforced', document: deploys,
    published_at: at(120 + i * 3 * day), published_by: 'user-maya', published_by_name: 'Maya Chen',
  })),
}

const sample = (minutesAgo: number, key: string, operation: string, decision: string) => ({
  observed_at: at(minutesAgo), key_id: `key-${key}`, key_name: key, operation,
  category: operation === 'createKey' ? 'credentials' : 'deploy', project: 'storefront', environment: 'production', decision,
})
const preview = {
  days: 7, evaluated: 1284, matched: 46, require_approval: 41, blocked: 5, exceptions: 0, evaluating_since: at(30 * day),
  samples: [
    sample(2, 'deploy-agent', 'createDeployment', 'require_approval'),
    sample(day, 'deploy-agent', 'deploySourceBuild', 'require_approval'),
    sample(4 * day, 'code-reviewer', 'rollbackApplication', 'require_approval'),
    sample(5 * day, 'security-triage', 'createKey', 'block'),
  ],
}
const match = { policy_id: 'pol-deploys', name: 'Production deploys need approval', version: 7, status: 'enforced', effect: 'require_approval', exception: false }
const simulation = { decision: 'require_approval', policy: match, matches: [match], reason: 'Matched every condition of Production deploys need approval v7.', category: 'deploy' }

const request = (
  id: string, key: string, owner: string, operation: string, method: string, path: string,
  state: string, minutesAgo: number, extra: Record<string, unknown> = {},
) => ({
  id, policy_id: 'pol-deploys', policy_name: 'Production deploys need approval', policy_version: 7,
  key_id: `key-${key}`, key_name: key, identity_id: `user-${owner.split(' ')[0].toLowerCase()}`, identity_name: owner,
  operation, category: 'deploy', method, path, project: 'storefront', environment: 'production',
  digest: `sha256:${id.padEnd(16, '0')}5f1c9a7e`, approvers: [{ kind: 'team', id: 'team-security' }, { kind: 'project_admins' }],
  state, decided_by: null, decided_by_name: null, decided_at: null, reason: '',
  expires_at: later(15 - minutesAgo), consumed_at: null, created_at: at(minutesAgo), can_decide: true, ...extra,
})
const decided = (name: string, minutesAgo: number, reason = '') =>
  ({ decided_by: `user-${name.split(' ')[0].toLowerCase()}`, decided_by_name: name, decided_at: at(minutesAgo), reason })
const pending = [
  request('apr-1', 'deploy-agent', 'Asha Patel', 'createDeployment', 'POST', '/api/applications/storefront/deployments', 'pending', 2),
  request('apr-2', 'code-reviewer', 'Daniel Okafor', 'rollbackApplication', 'POST', '/api/applications/checkout/rollback', 'pending', 6),
  request('apr-3', 'review-owner-ci', 'Review Owner', 'deploySourceBuild', 'POST', '/api/applications/docs/source-builds', 'pending', 9, { can_decide: false }),
]
const done = [
  request('apr-4', 'deploy-agent', 'Asha Patel', 'createDeployment', 'POST', '/api/applications/storefront/deployments', 'consumed', 3 * 60, { ...decided('Maya Chen', 175, 'Release 4.2'), consumed_at: at(170) }),
  request('apr-5', 'code-reviewer', 'Daniel Okafor', 'rollbackApplication', 'POST', '/api/applications/checkout/rollback', 'approved', 7 * 60, decided('Priya Raman', 410)),
  request('apr-6', 'security-triage', 'Priya Raman', 'deploySourceBuild', 'POST', '/api/applications/admin/source-builds', 'denied', day, decided('Maya Chen', day - 5, 'Not during the incident')),
  request('apr-7', 'deploy-agent', 'Asha Patel', 'createDeployment', 'POST', '/api/applications/billing/deployments', 'expired', 2 * day, { expires_at: at(2 * day - 15) }),
]
const approvals = (state: string | null) =>
  ({ items: scenario === 'empty' ? [] : state === 'decided' ? done : pending, next_cursor: '', pending: scenario === 'empty' ? 0 : pending.length })

const identity = {
  id: 'synthetic-agent-policies-review-owner', name: 'Review Owner', email: 'owner@example.invalid',
  admin: true, owner: true, credential_type: 'browser',
  permissions: ['deployments:read', 'deployments:write', 'logs:read'],
  project_roles: [{ project: 'review-project', role: 'admin' }], host_permissions: [], avatar_url: '', mfa_required: false,
}
const fixture = { synthetic: true, ready: false, scenario, requests: [] as string[], unknown: [] as string[] }
Object.assign(window, { __agentPoliciesFixture: fixture })
console.info(`Synthetic UI fixture (${scenario}): all API responses are invented. No live installation is connected.`)

const answers: Record<string, unknown> = {
  '/api/me': identity,
  '/api/projects': {
    items: [
      { id: 'proj-storefront', name: 'storefront', display_name: 'Storefront', personal: false, environments: [{ name: 'production' }, { name: 'staging' }] },
      { id: 'proj-finance', name: 'finance', display_name: 'Finance', personal: false, environments: [{ name: 'eu-production' }, { name: 'eu-staging' }] },
    ],
  },
  '/api/auth/status': { setup_required: false, deployment_mode: 'self-hosted', password: true, signup_enabled: false, providers: [], passkeys: true, totp: true, email_delivery: false },
  '/api/license': { valid: licensed, plan: licensed ? 'pro' : 'community', state: licensed ? 'active' : 'none', installation_id: 'synthetic-installation', revision: 1, edition: licensed ? 'pro' : 'community', catalog: [] },
  '/api/security/advisories': { feed: { feed_enabled: true, osv_enabled: true, revision: 0, issued_at: null, expires_at: null, checked_at: null, stale: false, error: '' }, applications: [], summary: { affected_applications: 0, critical: 0, high: 0 } },
  '/api/alarms': { items: [], next_cursor: '', summary: { active: 0, unread: 0 } },
  '/api/teams': { items: [{ id: 'team-security', name: 'Security on-call' }, { id: 'team-platform', name: 'Platform' }, { id: 'team-agents', name: 'Agents' }] },
  '/api/keys': { items: ['deploy-agent', 'code-reviewer', 'security-triage', 'review-owner-ci'].map((name) => ({ id: `key-${name}`, name })) },
  '/api/users': { items: [{ id: 'user-maya', name: 'Maya Chen', email: 'maya@example.invalid' }, { id: 'user-priya', name: 'Priya Raman', email: 'priya@example.invalid' }] },
  '/api/agent-policies': list,
  '/api/agent-policies/pol-deploys/versions': versions,
}
const fail = (status: number, code: string, message: string) => Response.json({ error: { code, message } }, { status })

window.fetch = async (input: RequestInfo | URL, init?: RequestInit) => {
  const request = input instanceof Request ? input : new Request(new URL(String(input), location.origin), init)
  const url = new URL(request.url)
  const method = init?.method || request.method
  const path = url.pathname
  fixture.requests.push(`${method} ${path}`)
  if (url.origin !== location.origin) throw new Error(`Synthetic fixture blocked ${url.origin}`)
  const agent = path.startsWith('/api/agent-')
  if (agent && scenario === 'error') return fail(503, 'unavailable', 'Synthetic fixture: the policy store is unavailable.')
  if (agent && !licensed && path !== '/api/agent-policies') return fail(402, 'license_required', 'Synthetic fixture: agent policies require Pro.')
  if (method === 'GET' && path === '/api/agent-approvals') return Response.json(approvals(url.searchParams.get('state')))
  if (method === 'POST' && path === '/api/agent-policies/preview') return Response.json(preview)
  if (method === 'POST' && path === '/api/agent-policies/simulate') return Response.json(simulation)
  if (method === 'GET' && path in answers) return Response.json(answers[path])
  const one = policies.find((p) => path === `/api/agent-policies/${p.id}`)
  if (method === 'GET' && one) return Response.json(one)
  if (method !== 'GET' && agent) return fail(409, 'synthetic_fixture', 'Synthetic fixture: changes are not saved in this review.')
  fixture.unknown.push(`${method} ${path}`)
  return fail(404, 'not_found', 'Synthetic fixture: not part of this review.')
}

const [{ DashboardShell }, settings, edit, create, approvalsPage] = await Promise.all([
  import('../../src/components/shell'),
  import('../../src/routes/settings'),
  import('../../src/routes/settings.agent-policies.$policyId'),
  import('../../src/routes/settings.agent-policies.new'),
  import('../../src/routes/approvals'),
])
const root = createRootRoute({ component: () => <DashboardShell><Outlet /></DashboardShell> })
const routes = [
  [settings.Route, '/settings'],
  [create.Route, '/settings/agent-policies/new'],
  [edit.Route, '/settings/agent-policies/$policyId'],
  [approvalsPage.Route, '/approvals'],
].map(([route, path]) => (route as typeof settings.Route).update({ id: path, path, getParentRoute: () => root } as never))
const router = createRouter({ routeTree: root.addChildren(routes), defaultPreload: false })
const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false, refetchOnWindowFocus: false, gcTime: 0 } } })
createRoot(document.getElementById('root')!).render(<QueryClientProvider client={queryClient}><RouterProvider router={router} /></QueryClientProvider>)
fixture.ready = true
