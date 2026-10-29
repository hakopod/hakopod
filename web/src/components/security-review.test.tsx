import assert from 'node:assert/strict'
import test from 'node:test'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import type { ApplicationSecurity, SecurityFinding } from '../lib/security'
import { SecurityReview } from './security-review'
import { overviewOf, routed } from './security-banner.test'

const finding = (over: Partial<SecurityFinding>): SecurityFinding => ({
  advisory_id: 'HKP-1',
  advisory_revision: 2,
  source: 'hakopod',
  severity: 'critical',
  title: 'Node.js request smuggling',
  url: 'https://example.com/HKP-1',
  service: 'web',
  confidence: 'confirmed',
  evidence: 'node 20.1.0',
  fix: 'reresolve',
  fix_reason: '',
  auto_eligible: false,
  fixed_digests: [],
  ...over,
})

const app = (over: Partial<ApplicationSecurity>): ApplicationSecurity => ({
  application_id: 'app-1',
  application_name: 'storefront',
  project: 'shop',
  environment: 'production',
  deployment_id: 'd1',
  revision: 4,
  checked_at: null,
  evaluated_at: null,
  findings: [finding({})],
  unknown_services: [],
  incomplete: [],
  approval_expires_at: null,
  auto_patch: false,
  approved_by: '',
  policy_revision: 1,
  attempts: [],
  can_write: true,
  ...over,
})

function render(applications: ApplicationSecurity[], expanded?: string) {
  const overview = { ...overviewOf(applications.length, 1), applications }
  return routed(
    <QueryClientProvider client={new QueryClient()}>
      <SecurityReview overview={overview} expanded={expanded} />
    </QueryClientProvider>,
  )
}

test('security review lists each affected app once with a clear summary', () => {
  const html = render([
    app({
      findings: [
        finding({}),
        finding({ service: 'api' }),
        finding({ advisory_id: 'HKP-2', severity: 'high' }),
      ],
    }),
  ])
  assert.match(html, /1 app affected · 1 critical advisory · 2 advisories in total/)
  assert.equal(html.match(/>storefront</g)?.length, 1)
  assert.match(html, /href="\/applications\/app-1"[^>]*>storefront/)
  assert.match(html, /shop · production/)
  assert.match(html, />1 critical</)
  assert.match(html, />1 high</)
  assert.match(html, /Runtime update available/)
  assert.match(html, /aria-expanded="false"/)
  assert.match(html, /Copy fix prompt/)
  assert.match(html, /Check again/)
  assert.doesNotMatch(html, /<table/)
})

test('an expanded row shows each advisory once with its services', () => {
  const html = render(
    [app({ findings: [finding({}), finding({ service: 'api' })] })],
    'app-1',
  )
  assert.match(html, /aria-expanded="true"/)
  assert.equal(html.match(/Node.js request smuggling/g)?.length, 1)
  assert.match(html, /web, api/)
  assert.match(html, /node 20.1.0/)
  assert.match(html, />Update web</)
  assert.match(html, />Update api</)
})

test('apps are ordered by their most severe advisories', () => {
  const html = render([
    app({ application_id: 'a', application_name: 'low-app', findings: [finding({ severity: 'low' })] }),
    app({ application_id: 'b', application_name: 'critical-app' }),
  ])
  assert.ok(html.indexOf('critical-app') < html.indexOf('low-app'))
})

test('manual finding shows its reason without a fix button', () => {
  const html = render(
    [app({ findings: [finding({ fix: 'manual', fix_reason: 'Upgrade the base image yourself.' })] })],
    'app-1',
  )
  assert.match(html, /Manual change needed/)
  assert.match(html, /Upgrade the base image yourself\./)
  assert.doesNotMatch(html, />Update /)
  assert.doesNotMatch(html, />Rebuild /)
})

test('read-only apps hide actions', () => {
  const html = render(
    [app({ can_write: false, findings: [finding({ auto_eligible: true })] })],
    'app-1',
  )
  assert.doesNotMatch(html, />Update /)
  assert.doesNotMatch(html, /Check again/)
  assert.doesNotMatch(html, /type="checkbox"/)
})

test('no affected apps shows the disclaimer', () => {
  const html = render([])
  assert.match(html, /No affected apps/)
  assert.match(html, /No findings does not mean an app is free of vulnerabilities\./)
})

test('incomplete check stays visible for an app without findings', () => {
  const html = render(
    [
      app({ findings: [], incomplete: ['web: image config unreadable'] }),
      app({
        application_id: 'app-2',
        application_name: 'billing',
        incomplete: ['OSV.dev timed out'],
      }),
    ],
    'app-1',
  )
  assert.match(html, /1 app affected · 1 critical advisory · 1 advisory in total · 2 incomplete checks · 1 app with no findings/)
  assert.ok(html.indexOf('billing') < html.indexOf('storefront'))
  assert.match(html, /No advisories found/)
  assert.match(html, /web: image config unreadable/)
  assert.match(html, /Check incomplete/)
})

test('sources show as one line with an edit action for managers', () => {
  const overview = { ...overviewOf(1, 1), applications: [app({})] }
  const html = routed(
    <QueryClientProvider client={new QueryClient()}>
      <SecurityReview
        overview={overview}
        settings={{ feed_enabled: true, osv_enabled: true, revision: 1, can_manage: true }}
      />
    </QueryClientProvider>,
  )
  assert.match(html, /Sources: Hakopod feed, OSV.dev/)
  assert.match(html, />Edit sources</)
  assert.doesNotMatch(html, /type="checkbox"/)
})
