import '../lib/build-onboarding.test'
import '../lib/effective-service.test'
import '../lib/dotenv.test'
import '../lib/save-environment.test'
import assert from 'node:assert/strict'
import test from 'node:test'
import './toml-code.test'
import './shell-behavior.test'
import './selection-navigation.test'
import './secret-providers.test'
import './installation-settings.test'
import './git-connections.test'
import './git-app-setup.test'
import './runtime-notice.test'
import '../lib/runtime-health.test'
import '../lib/lifecycle.test'
import '../lib/remove-service.test'
import '../lib/projects.test'
import '../lib/runtime-metrics.test'
import { renderToStaticMarkup } from 'react-dom/server'
import { Copy, ErrorState } from './shared'
import { APIError, message } from '../lib/api'
import { specToTOML } from '../lib/toml'
import { serviceProfileLabel, serviceResources } from '../lib/service-resources'
import { canAccess, canOpenHostTerminal } from '../lib/scope'
import type { Identity } from '../lib/types'

test('error messages survive normalization and rendering across form boundaries', () => {
  const text = 'Verify domain ownership before importing an application with custom domains.'
  const cause = new APIError(text, 400, 'domain_verification_required')
  assert.equal(message(message(cause)), text)
  const recovery = renderToStaticMarkup(<ErrorState error={cause} retry={() => {}} />)
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
        tls: { issuer: 'letsencrypt-staging' },
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
