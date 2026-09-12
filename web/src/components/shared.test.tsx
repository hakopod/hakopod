import assert from 'node:assert/strict'
import test from 'node:test'
import './toml-code.test'
import { renderToStaticMarkup } from 'react-dom/server'
import { Copy } from './shared'
import { specToTOML } from '../lib/toml'
import { canAccess, canOpenHostTerminal } from '../lib/scope'
import type { Identity } from '../lib/types'

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
