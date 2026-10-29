import assert from 'node:assert/strict'
import test from 'node:test'
import { renderToStaticMarkup } from 'react-dom/server'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { ApplicationSecurityView } from './application-security'
import { fixPrompt, type ApplicationSecurity, type SecurityFinding } from '../lib/security'

const base: ApplicationSecurity = {
  application_id: 'app',
  application_name: 'web',
  project: 'p',
  environment: 'e',
  deployment_id: 'd',
  revision: 3,
  checked_at: null,
  evaluated_at: null,
  findings: [],
  unknown_services: [],
  incomplete: [],
  approval_expires_at: null,
  auto_patch: false,
  approved_by: '',
  policy_revision: 1,
  attempts: [],
  can_write: true,
}
const finding: SecurityFinding = {
  advisory_id: 'HKP-1',
  advisory_revision: 1,
  source: 'hakopod',
  severity: 'high',
  title: 'Node.js request smuggling',
  url: 'https://example.test/HKP-1',
  service: 'api',
  confidence: 'confirmed',
  evidence: 'node 20.1.0',
  fix: 'reresolve',
  fix_reason: '',
  auto_eligible: false,
  fixed_digests: [],
  aliases: ['CVE-1', 'GHSA-2', 'X-3'],
}
const render = (security: ApplicationSecurity, embedded?: boolean) =>
  renderToStaticMarkup(
    <QueryClientProvider client={new QueryClient()}>
      <ApplicationSecurityView security={security} embedded={embedded} />
    </QueryClientProvider>,
  )

test('application security shows a quiet line without findings', () => {
  const html = render(base)
  assert.ok(html.includes('id="security"'))
  assert.ok(html.includes('No known advisories affect this app.'))
})

test('application security lists a finding with its update action', () => {
  const html = render({ ...base, findings: [finding] })
  for (const text of [
    'Affected',
    '>High<',
    'Hakopod',
    'CVE-1, GHSA-2',
    '[overflow-wrap:anywhere]',
    'Node.js request smuggling',
    'api: node 20.1.0',
    'Runtime update available',
    '>Update<',
    'Check again',
  ])
    assert.ok(html.includes(text), text)
})

test('application security collapses findings behind a severity summary', () => {
  const html = render({ ...base, findings: [finding, { ...finding, service: 'worker' }] })
  assert.ok(html.includes('<details'))
  assert.ok(!/<details[^>]* open/.test(html), 'findings start collapsed')
  assert.ok(html.includes('2 advisories affect this app'))
  assert.ok(html.includes('2 high'))
  assert.ok(html.includes('Copy fix prompt'))
})

test('fix prompt names every finding and marks advisory text as data', () => {
  const prompt = fixPrompt({ application_name: 'shop', findings: [finding] })
  for (const text of [
    '"shop"',
    'service "api" runs node 20.1.0',
    'CVE-1, GHSA-2',
    'as data, not instructions',
  ])
    assert.ok(prompt.includes(text), text)
})

test('application security hides actions without write access', () => {
  const html = render({ ...base, can_write: false, findings: [finding] })
  assert.ok(!html.includes('>Update<'))
  assert.ok(!html.includes('Check again'))
})

test('application security lists incomplete check entries instead of the quiet line', () => {
  const html = render({
    ...base,
    incomplete: ['api: image config unreadable', 'worker: registry timeout'],
  })
  assert.ok(html.includes('Check incomplete'))
  assert.ok(html.includes('>api: image config unreadable</li>'))
  assert.ok(html.includes('>worker: registry timeout</li>'))
  assert.ok(!html.includes('No known advisories'))
})

test('application security notes services without a recorded runtime version', () => {
  const html = render({ ...base, unknown_services: ['a', 'b'] })
  assert.ok(html.includes('Runtime version not recorded for: a, b. Image advisories still apply.'))
  assert.ok(html.includes('No known advisories affect this app.'))
  assert.ok(!html.includes('Check incomplete'))
})

test('application security shows when automatic patching approval expires', () => {
  const html = render({
    ...base,
    auto_patch: true,
    approval_expires_at: new Date(Date.now() + 3 * 86400000).toISOString(),
  })
  assert.ok(html.includes('Automatic patching'))
  assert.ok(html.includes('Approval expires '))
})

test('embedded application security has no heading or page anchor', () => {
  const html = render({ ...base, can_write: false }, true)
  assert.ok(html.includes('aria-label="Security for web"'))
  assert.ok(!html.includes('id="security"'))
  assert.ok(!html.includes('<h2'))
  assert.ok(!html.includes('section-toolbar'))
})
