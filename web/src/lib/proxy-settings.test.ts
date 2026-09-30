import assert from 'node:assert/strict'
import test from 'node:test'
import {
  edgeDraft,
  editableProxySettings,
  prepareEdgePolicy,
  prepareProxySettings,
  sameEdgePolicy,
  type EdgePolicy,
  type ProxyStatus,
} from './proxy-settings.ts'

// Synthetic controller settings; these tests never contact an installation.
const snapshot: ProxyStatus = {
  revision: 4,
  drift: false,
  change: { status: 'applied', error: '' },
  observed: {
    namespace: 'hakopod-dev-ingress',
    name: 'haproxy',
    resource_version: '17',
    edge: { enabled: false, client_ip_source: 'connection', rules: [] },
    settings: { maxconn: '1024', 'timeout-connect': '5s', 'operator-key': 'keep' },
    fields: [
      { name: 'maxconn', description: 'Connection limit', example: '1024' },
      { name: 'timeout-connect', description: 'Connection timeout', example: '5s' },
    ],
  },
}

test('the Edge editor changes only explicit values and preserves controller defaults', () => {
  assert.deepEqual(editableProxySettings(snapshot), {
    maxconn: '1024',
    'timeout-connect': '5s',
  })
  assert.deepEqual(prepareProxySettings('{"maxconn":"2048"}', snapshot), { maxconn: '2048' })
  assert.deepEqual(prepareProxySettings('{"timeout-connect":""}', snapshot), {
    'timeout-connect': '',
  })
  assert.deepEqual(prepareProxySettings('{"maxconn":"1024"}', snapshot), {})
  assert.deepEqual(prepareProxySettings('{}', snapshot), {})
  assert.equal(snapshot.observed.settings['operator-key'], 'keep')
})

test('the Edge editor rejects unsupported directives and malformed settings before review', () => {
  for (const input of ['null', '[]', '1', 'true', '{"maxconn":2048}'])
    assert.throws(() => prepareProxySettings(input, snapshot), /object with string values/)
  assert.throws(() => prepareProxySettings('{', snapshot), /valid JSON/)
  assert.throws(
    () => prepareProxySettings('{"frontend-config-snippet":"http-request deny"}', snapshot),
    /Unsupported controller setting/,
  )
  assert.throws(() => prepareProxySettings('{"maxconn":" 2048"}', snapshot), /spaces/)
  assert.throws(
    () => prepareProxySettings(JSON.stringify({ maxconn: '1'.repeat(33) }), snapshot),
    /32 characters/,
  )
})

const policy: EdgePolicy = {
  enabled: true,
  client_ip_source: 'connection',
  rules: [
    { id: 'login', host: 'app.example.test', path_prefix: '/login', requests_per_second: 5 },
    { id: 'api', host: 'app.example.test', path_prefix: '/', deny_cidrs: ['192.0.2.0/24'] },
  ],
}

test('Edge drafts normalize optional fields without inventing a policy change', () => {
  const draft = edgeDraft(policy)
  assert.equal(draft.client_ip_header, '')
  assert.equal(draft.country_header, '')
  assert.equal(draft.rules[1].requests_per_second, '0')
  assert.equal(draft.rules[0].allow_cidrs, '')
  assert.equal(sameEdgePolicy(policy, prepareEdgePolicy(draft)), true)
  assert.deepEqual(
    prepareEdgePolicy(draft).rules.map((rule) => rule.id),
    ['login', 'api'],
  )
  draft.rules.reverse()
  assert.deepEqual(
    prepareEdgePolicy(draft).rules.map((rule) => rule.id),
    ['api', 'login'],
  )
  assert.equal(sameEdgePolicy(policy, prepareEdgePolicy(draft)), false)
  assert.equal(policy.rules[0].id, 'login', 'editing a draft does not mutate observed data')
})

test('trusted Edge headers require scoped peers and country rules require a country source', () => {
  const draft = edgeDraft(policy)
  draft.client_ip_source = 'trusted_proxy'
  assert.throws(() => prepareEdgePolicy(draft), /trusted proxy network/)
  draft.client_ip_header = 'X-Real-IP'
  draft.trusted_proxy_cidrs = '192.0.2.0/24\n2001:db8::/32'
  draft.rules[0].allow_countries = 'us, de'
  assert.throws(() => prepareEdgePolicy(draft), /country header/)
  draft.country_header = 'CloudFront-Viewer-Country'
  const prepared = prepareEdgePolicy(draft)
  assert.deepEqual(prepared.rules[0].allow_countries, ['US', 'DE'])
  assert.equal(draft.rules[0].allow_countries, 'us, de', 'validation preserves the entered draft')
  for (const cidr of ['0.0.0.0/0', '::/0']) {
    draft.trusted_proxy_cidrs = cidr
    assert.throws(() => prepareEdgePolicy(draft), /entire internet/)
  }
  draft.trusted_proxy_cidrs = '192.0.2.1'
  draft.rules[0].allow_countries = ''
  draft.client_ip_source = 'connection'
  const direct = prepareEdgePolicy(draft)
  assert.equal(direct.client_ip_header, '')
  assert.equal(direct.country_header, '')
  assert.deepEqual(direct.trusted_proxy_cidrs, [])
  assert.equal(
    draft.trusted_proxy_cidrs,
    '192.0.2.1',
    'hidden proxy inputs remain in the local draft',
  )
})

test('Edge review rejects ambiguous networks and unsafe route selectors while disabled too', () => {
  const draft = edgeDraft({ ...policy, enabled: false })
  for (const address of [
    '256.0.0.1',
    '192.0.2.1/33',
    '2001:db8::/129',
    '::ffff:192.0.2.1',
    'fe80::1%lo0',
  ]) {
    draft.rules[0].allow_cidrs = address
    assert.throws(() => prepareEdgePolicy(draft), /IPv4 or IPv6/)
  }
  draft.rules[0].allow_cidrs = '192.0.2.1\n2001:db8::1'
  assert.deepEqual(prepareEdgePolicy(draft).rules[0].allow_cidrs, ['192.0.2.1', '2001:db8::1'])
  for (const host of [
    '*.example.test',
    'https://app.example.test',
    'app.example.test:443',
    'app..example.test',
  ]) {
    draft.rules[0].host = host
    assert.throws(() => prepareEdgePolicy(draft), /exact hostname/)
  }
  draft.rules[0].host = 'APP.example.test'
  assert.equal(prepareEdgePolicy(draft).rules[0].host, 'app.example.test')
  for (const path of [
    '/api%2Flogin',
    '//login',
    '/a/../login',
    '/a/./login',
    '/login?mode=test',
    '/' + 'a'.repeat(128),
  ]) {
    draft.rules[0].path_prefix = path
    assert.throws(() => prepareEdgePolicy(draft), /path of up to 128/)
  }
  draft.rules[0].path_prefix = '/login'
  for (const rate of ['', '-1', '1.5', '100001', 'Infinity']) {
    draft.rules[0].requests_per_second = rate
    assert.throws(() => prepareEdgePolicy(draft), /request limit/)
  }
  draft.rules[0].requests_per_second = '0'
  draft.rules[1].id = 'login'
  assert.throws(() => prepareEdgePolicy(draft), /unique ID/)
  draft.rules[1].id = 'api'
  draft.rules[1].path_prefix = '/login'
  assert.throws(() => prepareEdgePolicy(draft), /repeats an existing hostname and path/)
})

test('Edge drafts enforce per-rule and whole-policy bounds before review', () => {
  const draft = edgeDraft(policy)
  draft.rules[0].deny_cidrs = Array.from({ length: 65 }, (_, i) => `192.0.2.${i}`).join('\n')
  assert.throws(() => prepareEdgePolicy(draft), /at most 64 entries/)
  const full = Array.from({ length: 64 }, (_, i) => `192.0.2.${i}`).join('\n')
  draft.rules = Array.from({ length: 9 }, (_, i) => ({
    ...draft.rules[0],
    key: String(i),
    id: `rule-${i}`,
    path_prefix: `/rule-${i}`,
    deny_cidrs: full,
  }))
  assert.throws(() => prepareEdgePolicy(draft), /at most 512/)
  draft.rules = Array.from({ length: 33 }, (_, i) => ({
    ...draft.rules[0],
    key: String(i),
    id: `rule-${i}`,
    path_prefix: `/rule-${i}`,
    deny_cidrs: '',
  }))
  assert.throws(() => prepareEdgePolicy(draft), /at most 32 traffic rules/)
})
