import '../lib/toml-language.test'
import '../lib/framework-recipes.test'
import '../lib/key-lifetime.test'
import '../lib/runner-resources.test'
import '../lib/public-endpoints.test'
import '../lib/database-public-endpoints.test'
import '../lib/build-onboarding.test'
import '../lib/effective-service.test'
import '../lib/dotenv.test'
import '../lib/save-environment.test'
import '../lib/proxy-settings.test'
import '../lib/tls-issuers.test'
import '../lib/toast-position.test'
import assert from 'node:assert/strict'
import test from 'node:test'
import './toml-code.test'
import './shell-behavior.test'
import './selection-navigation.test'
import './secret-providers.test'
import './installation-settings.test'
import './git-connections.test'
import './git-app-setup.test'
import './deployment-secrets.test'
import './managed-actions-form.test'
import './managed-actions-workflows.test'
import './managed-actions-cancel.test'
import './managed-actions-hold.test'
import './runtime-notice.test'
import '../lib/runtime-health.test'
import '../lib/lifecycle.test'
import '../lib/remove-service.test'
import '../lib/projects.test'
import '../lib/runtime-metrics.test'
import '../lib/database-view.test'
import '../lib/database-topology.test'
import '../lib/database-create.test'
import '../lib/database-monitoring.test'
import '../lib/external-databases.test'
import '../lib/database-placement.test'
import { renderToStaticMarkup } from 'react-dom/server'
import { Copy, ErrorState } from './shared'
import { APIError, message } from '../lib/api'
import { specToTOML } from '../lib/toml'
import { serviceProfileLabel, serviceResources } from '../lib/service-resources'
import { canAccess, canOpenHostTerminal } from '../lib/scope'
import type { Identity } from '../lib/types'
import { engineManagedSource } from '../lib/backups'

test('managed ClickHouse recovery jobs do not inherit application-engine object-storage status', () => {
  assert.equal(engineManagedSource({ kind: 'managed_database', engine: 'clickhouse' }), false)
  assert.equal(engineManagedSource({ kind: 'database', engine: 'clickhouse' }), true)
  assert.equal(engineManagedSource(undefined), false)
})

test('error messages survive normalization and rendering across form boundaries', () => {
  const text = 'Verify domain ownership before importing an application with custom domains.'
  const cause = new APIError(text, 400, 'domain_verification_required')
  assert.equal(message(message(cause)), text)
  const recovery = renderToStaticMarkup(<ErrorState error={cause} retry={() => {}} />)
  assert.ok(recovery.includes(text), 'recovery instructions remain visible after the toast closes')
  assert.ok(recovery.includes('Show error'))
  assert.ok(recovery.includes('Retry'))
  assert.ok(
    !recovery.includes('role="alert"'),
    'SSR recovery does not create an empty alert banner',
  )
  assert.equal(message(new Error('Connection failed.')), 'Connection failed.')
  for (const value of ['', '  ', undefined, null, {}, 0, new Error('')])
    assert.equal(message(value), 'An unexpected error occurred.')
})

test('copying a generated key cannot submit its credential creation form', () => {
  const html = renderToStaticMarkup(
    <form method="post">
      <Copy value="test-value-not-a-credential" label="Copy key" />
    </form>,
  )
  const button = html.match(/<button\b[^>]*>/)?.[0]
  assert.ok(button, 'the generated-key control renders a real button')
  // A button without an explicit type submits its containing form by default.
  assert.match(button, /\btype="button"/, 'copy must have no form submission default action')
  assert.ok(html.includes('Copy key'))
  assert.equal(html.includes('test-value-not-a-credential'), false)
})

// Configuration editing must retain non-form service settings.
test('canonical configuration preserves persistent volumes, GPU, TLS and secret bindings', () => {
  const output = specToTOML({
    schema_version: 1,
    name: 'example',
    domains: { 'app.example.com': 'web' },
    services: {
      web: {
        image: 'registry.example/app@sha256:' + 'a'.repeat(64),
        registry_credential: 'private',
        restart_nonce: 'once',
        run_as_user: 1000,
        volume: { mount_path: '/data', size_gib: 10, storage_class: 'local-path' },
        resources: { cpu_request: '250m', cpu_limit: '2', memory_limit: '1Gi' },
        gpu: { count: 1 },
        tls: { issuer: 'letsencrypt-staging', issuer_kind: 'Issuer' },
        secrets: { API_TOKEN: { ref: 'api-token' } },
      },
    },
  })
  for (const expected of [
    '[domains]',
    '"app.example.com" = "web"',
    'registry_credential = "private"',
    'restart_nonce = "once"',
    'run_as_user = 1000',
    '[services.web.resources]',
    'cpu_request = "250m"',
    'cpu_limit = "2"',
    'memory_limit = "1Gi"',
    '[services.web.volume]',
    'mount_path = "/data"',
    'size_gib = 10',
    '[services.web.gpu]',
    'count = 1',
    '[services.web.tls]',
    'issuer = "letsencrypt-staging"',
    'issuer_kind = "Issuer"',
    '[services.web.secrets.API_TOKEN]',
    'ref = "api-token"',
  ])
    assert.ok(output.includes(expected), expected)
})

test('human session envelopes do not override project membership roles', () => {
  const identity: Identity = {
    id: 'human',
    name: 'Member',
    admin: false,
    owner: false,
    credential_type: 'browser',
    permissions: ['admin'],
    project: '',
    environment: '',
    project_roles: [
      { project: 'editable', role: 'developer' },
      { project: 'observable', role: 'viewer' },
    ],
  }
  assert.equal(canAccess(identity, 'editable', 'deployments:write'), true)
  assert.equal(canAccess(identity, 'observable', 'deployments:write'), false)
  assert.equal(canAccess(identity, 'observable', 'logs:read'), true)
  assert.equal(canAccess(identity, 'other-project', 'deployments:read'), false)
})

test('TOML export retains network denials, mount permissions and advanced runtime settings', () => {
  const output = specToTOML({
    schema_version: 1,
    name: 'runtime-example',
    networks: { private: { internal: true } },
    volumes: { data: { size_gib: 10, access_mode: 'ReadWriteOnce' } },
    services: {
      api: {
        image: 'registry.example/api@sha256:' + 'b'.repeat(64),
        networks: ['private'],
        network_access: { from: [] },
        private_egress: ['orders-db'],
        ports: [{ name: 'metrics', port: 9090, target_port: 9091, protocol: 'TCP' }],
        mounts: [{ volume: 'data', mount_path: '/data', read_only: true, sub_path: 'archive' }],
        temporary_mounts: [{ mount_path: '/tmp', size_mib: 16, memory: true }],
        read_only_root_filesystem: true,
        run_as_user: 12345,
        run_as_group: 23456,
        fs_group: 23456,
        working_dir: '/data',
        termination_grace_seconds: 45,
      },
    },
  })
  assert.match(output, /\[services\.api\.network_access\]\nfrom = \[\]/)
  assert.match(output, /\[networks\.private\]\ninternal = true/)
  assert.match(output, /\[volumes\.data\]\nsize_gib = 10\naccess_mode = "ReadWriteOnce"/)
  for (const setting of [
    '"target_port" = 9091',
    '"protocol" = "TCP"',
    '"read_only" = true',
    '"sub_path" = "archive"',
    '"size_mib" = 16',
    '"memory" = true',
    'read_only_root_filesystem = true',
    'run_as_user = 12345',
    'run_as_group = 23456',
    'fs_group = 23456',
    'working_dir = "/data"',
    'termination_grace_seconds = 45',
    'private_egress = ["orders-db"]',
  ])
    assert.ok(output.includes(setting), `Export lost ${setting}`)
})

test('host terminal controls require owner or explicit node authority, never admin alone', () => {
  const admin: Identity = {
    id: 'admin',
    name: 'Administrator',
    admin: true,
    owner: false,
    credential_type: 'browser',
    permissions: ['admin'],
    project: '',
    environment: '',
  }
  assert.equal(canOpenHostTerminal(admin, 'node-a'), false)
  assert.equal(canOpenHostTerminal({ ...admin, owner: true }, 'node-a'), true)
  const delegated = {
    ...admin,
    host_permissions: [{ node: 'node-a', permission: 'nodes:terminal' }],
  }
  assert.equal(canOpenHostTerminal(delegated, 'node-a'), true)
  assert.equal(canOpenHostTerminal(delegated, 'node-b'), false)
  assert.equal(
    canOpenHostTerminal(
      { ...admin, host_permissions: [{ node: '*', permission: 'nodes:terminal' }] },
      'node-b',
    ),
    true,
  )
  assert.equal(canOpenHostTerminal({ ...delegated, credential_type: 'machine' }, 'node-a'), false)
})
import '../lib/service-environment.test'
import '../lib/virtual-networks.test'

test('installation prerequisites link to setup without losing the error', () => {
  for (const text of [
    'SMTP readiness probe image is missing',
    'persistent storage is unavailable',
    'The maintenance service is unavailable',
  ]) {
    const html = renderToStaticMarkup(<ErrorState error={text} />)
    assert.ok(html.includes('Show error'))
    assert.ok(html.includes('/infrastructure?tab=setup'))
  }
})

test('resource review combines explicit values with the selected size defaults', () => {
  const service = { image: 'nginx:alpine', size: 'medium' }
  assert.equal(serviceProfileLabel({ ...service, resources: { cpu_limit: '2' } }), 'Custom')
  assert.equal(serviceProfileLabel({ ...service, resources: { memory_request: '' } }), 'medium')
  assert.equal(serviceProfileLabel({ image: service.image }), 'small')

  assert.deepEqual(
    serviceResources(
      { image: 'nginx:alpine', size: 'medium', resources: { cpu_limit: '2', memory_request: '' } },
      {
        medium: { CPURequest: '250m', CPULimit: '1', MemoryRequest: '256Mi', MemoryLimit: '512Mi' },
      },
    ),
    { CPURequest: '250m', CPULimit: '2', MemoryRequest: '256Mi', MemoryLimit: '512Mi' },
  )
})

import '../lib/service-volume-removal.test'
import '../lib/actions-logs.test'

// Log text must not retain executable terminal hyperlinks or conceal payloads
// when users copy/download the displayed window.
import {
  cleanWorkflowLog,
  logTimestamp,
  groupWorkflowLines,
  workflowLogPage,
  reconcileWorkflowExpansion,
  workflowLogDownload,
} from '../lib/actions-logs'
test('workflow logs strip terminal controls and preserve literal markup', () => {
  assert.equal(
    cleanWorkflowLog('\u001b[31merror\u001b[0m <script>literal</script>'),
    'error <script>literal</script>',
  )
  assert.equal(
    cleanWorkflowLog('\u001b]8;;https://example.invalid\u0007label\u001b]8;;\u0007'),
    'label',
  )
  assert.equal(logTimestamp('2026-09-29T00:00:00.000Z output')?.text, 'output')
  assert.equal(logTimestamp('not a timestamp'), null)
  assert.equal(logTimestamp('2026-99-99T99:99:99.000Z invalid'), null)
})

test('workflow groups preserve rows, nested failures and unfinished output', () => {
  const lines = [
    '##[group]Docker info',
    'details',
    '##[endgroup]',
    '##[group]QEMU',
    '##[group]Install',
    'error: no such device',
    '##[endgroup]',
    '##[endgroup]',
    '##[group]Live',
    '<script>literal</script>',
  ].map((text, number) => ({ number: number + 1, text: `2026-09-29T00:00:00.000Z ${text}` }))
  const { rows, groups } = groupWorkflowLines(lines)
  assert.equal(groups.get(1)?.attention, false)
  assert.equal(groups.get(1)?.count, 1)
  assert.equal(groups.get(4)?.attention, true)
  assert.equal(groups.get(5)?.attention, true)
  assert.equal(groups.get(9)?.unfinished, true)
  assert.deepEqual(rows.find((line) => line.number === 6)?.parents, [4, 5])
  assert.equal(rows.at(-1)?.text, lines.at(-1)?.text)
  assert.equal(
    rows.some((line) => line.text.endsWith('##[endgroup]')),
    false,
  )
})
test('workflow group window keeps unmatched delimiters and bounds nesting', () => {
  assert.equal(groupWorkflowLines([{ number: 1, text: '##[endgroup]' }]).rows.length, 1)
  const lines = Array.from({ length: 1000 }, (_, number) => ({ number, text: '##[group]Nested' }))
  assert.ok(groupWorkflowLines(lines).rows.every((row) => row.parents.length <= 32))
})

test('workflow paging retains full group context and caps rows including search ancestors', () => {
  const lines = [
    '##[group]Large build',
    ...Array.from({ length: 2400 }, (_, i) => `output ${i}`),
    '##[endgroup]',
  ].map((text, i) => ({ number: i + 1, text }))
  const parsed = groupWorkflowLines(lines)
  let page = workflowLogPage(parsed, '', null)
  assert.equal(page.rows[0].number, 1)
  assert.equal(parsed.groups.get(1)?.count, 2400)
  assert.equal(parsed.groups.get(1)?.unfinished, false)
  assert.ok(page.rows.length <= 1000)
  const lastStart = page.start
  const earlier = workflowLogPage(parsed, '', lastStart)
  assert.equal(earlier.end, lastStart)
  assert.equal(workflowLogPage(parsed, '', earlier.end + 1).start, lastStart)
  const nested = Array.from({ length: 500 }, (_, i) => [
    `##[group]Outer ${i}`,
    '##[group]Inner',
    'needle',
    '##[endgroup]',
    '##[endgroup]',
  ])
    .flat()
    .map((text, i) => ({ number: i + 1, text }))
  const tree = groupWorkflowLines(nested)
  page = workflowLogPage(tree, 'needle', null)
  assert.ok(page.rows.length <= 1000)
  assert.equal(page.total, 500)
  for (const row of page.rows)
    for (const id of row.parents) assert.ok(page.rows.some((parent) => parent.number === id))
})

test('workflow expansion survives closing live groups and resets identity and source state', () => {
  const parse = (texts: string[]) =>
    groupWorkflowLines(texts.map((text, i) => ({ number: i + 1, text })))
  const live = parse(['##[group]Build', 'compiling'])
  let state = reconcileWorkflowExpansion({}, live, false, false)
  assert.equal(state[1].open, true)
  const complete = parse(['##[group]Build', 'compiling', '##[endgroup]'])
  state = reconcileWorkflowExpansion(state, complete, false, false)
  assert.equal(state[1].open, true)
  state[1].open = false
  state = reconcileWorkflowExpansion(state, complete, true, false)
  assert.equal(state[1].open, true, 'job failure exposes diagnostics regardless of wording')
  state[1].open = false
  state = reconcileWorkflowExpansion(
    state,
    parse(['##[group]Build', '##[error]new failure', '##[endgroup]']),
    true,
    true,
  )
  assert.equal(state[1].open, true, 'new annotation exposes a previously closed group')
  assert.equal(
    reconcileWorkflowExpansion(
      state,
      parse(['##[group]Different source', 'okay', '##[endgroup]']),
      false,
      false,
    )[1].open,
    false,
  )
  assert.deepEqual(reconcileWorkflowExpansion(state, parse(['ordinary output']), false, false), {})
  assert.equal(
    reconcileWorkflowExpansion({}, complete, false, false)[1].open,
    false,
    'source reset starts fresh',
  )
})

test('workflow parsing and state remain bounded and download retains original source text', () => {
  const huge = 'a'.repeat(65536)
  const lines = [
    { number: 1, text: '##[group]Build' },
    { number: 2, text: huge, rawText: '\x1b[31m' + huge },
    { number: 3, text: '##[endgroup]' },
  ]
  assert.equal(workflowLogPage(groupWorkflowLines(lines), '', null).rows[1].text.length, 65536)
  assert.equal(workflowLogDownload(lines), '##[group]Build\n\x1b[31m' + huge + '\n##[endgroup]')
  const many = groupWorkflowLines(
    Array.from({ length: 10001 }, (_, i) => [
      { number: i * 2, text: '##[group]g' },
      { number: i * 2 + 1, text: '##[endgroup]' },
    ]).flat(),
  )
  assert.equal(Object.keys(reconcileWorkflowExpansion({}, many, false, false)).length, 10000)
})

import { observedServiceImage } from '../lib/service-image'
import type { Service, ServiceStatus } from '../lib/types'
test('managed image display requires observed runtime images while ordinary services retain fallback', () => {
  const ordinary = { image: 'saved-image' } as Service
  const managed = { ...ordinary, actions: { repository: 'fixture/repo' } } as Service
  const old = { image: 'historical-scalar' } as ServiceStatus
  assert.equal(observedServiceImage(ordinary), 'saved-image')
  assert.equal(observedServiceImage(ordinary, old), 'historical-scalar')
  assert.equal(observedServiceImage(managed), undefined)
  assert.equal(observedServiceImage(managed, old), undefined)
  assert.equal(
    observedServiceImage(managed, { ...old, images: ['old-runtime', 'new-runtime'] }),
    'old-runtime, new-runtime',
  )
})

test('new failure reopens a group already marked by an earlier warning', () => {
  const parse = (extra: string[]) =>
    groupWorkflowLines(
      ['##[group]Build', '##[warning]Earlier warning', ...extra, '##[endgroup]'].map(
        (text, number) => ({ number, text }),
      ),
    )
  const first = parse([])
  const previous = reconcileWorkflowExpansion({}, first, false, false)
  previous[0].open = false
  assert.equal(reconcileWorkflowExpansion(previous, first, false, false)[0].open, false)
  assert.equal(
    reconcileWorkflowExpansion(previous, parse(['npm ERR! later failure']), false, false)[0].open,
    true,
  )
})
