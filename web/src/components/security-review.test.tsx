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

function render(applications: ApplicationSecurity[]) {
  const overview = { ...overviewOf(applications.length, 1), applications }
  return routed(
    <QueryClientProvider client={new QueryClient()}>
      <SecurityReview overview={overview} />
    </QueryClientProvider>,
  )
}

test('security review shows affected card copy', () => {
  const html = render([app({ findings: [finding({}), finding({ advisory_id: 'HKP-2' })] })])
  assert.match(html, /Affected apps/)
  assert.match(html, /storefront/)
  assert.match(html, /shop · production/)
  assert.match(html, />Affected</)
  assert.match(html, /Runtime update available/)
  assert.match(html, /2 advisories/)
  assert.match(html, /href="\/security\?application=app-1"/)
  assert.match(html, /id="advisory-app-1"/)
  assert.match(html, /href="\/applications\/app-1"[^>]*>Open app/)
  assert.match(html, />Update</)
  assert.match(html, /Security advisories go to app notification channels that include them\./)
  assert.match(html, /Automatic patching is off by default\./)
})

test('manual finding shows its reason without a fix button', () => {
  const html = render([
    app({ findings: [finding({ fix: 'manual', fix_reason: 'Upgrade the base image yourself.' })] }),
  ])
  assert.match(html, /Upgrade the base image yourself\./)
  assert.doesNotMatch(html, />Update</)
  assert.doesNotMatch(html, />Rebuild</)
})

test('read-only apps hide actions', () => {
  const html = render([app({ can_write: false, findings: [finding({ auto_eligible: true })] })])
  assert.doesNotMatch(html, />Update</)
  assert.doesNotMatch(html, /Check again/)
  assert.doesNotMatch(html, /Automatic patching<\/label>|type="checkbox"/)
})

test('no affected apps shows the disclaimer', () => {
  const html = render([])
  assert.match(html, /No affected apps/)
  assert.match(html, /No findings does not mean an app is free of vulnerabilities\./)
})

test('incomplete check stays visible for an app without findings', () => {
  const html = render([
    app({ findings: [], incomplete: ['web: image config unreadable'] }),
    app({
      application_id: 'app-2',
      application_name: 'billing',
      incomplete: ['OSV.dev timed out'],
    }),
  ])
  assert.match(html, /Affected apps <span[^>]*>1</)
  assert.match(html, /Incomplete checks <span[^>]*>1</)
  assert.match(html, /<dt>Incomplete checks<\/dt><dd>2<\/dd>/)
  assert.match(html, /storefront/)
  assert.match(html, /web: image config unreadable/)
  assert.match(html, /OSV.dev timed out/)
  assert.doesNotMatch(html, /0 advisories/)
})
