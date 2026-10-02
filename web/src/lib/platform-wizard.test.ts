import assert from 'node:assert/strict'
import test from 'node:test'
import { completePlatformSpec, platformSteps } from './platform-wizard'
import type { ManagedPlatformDefaults } from './managed-platforms'

const draft = {
  schema_version: 1,
  name: 'customer-platform',
  kind: 'supabase',
  version: '0.8.2',
  resources: {},
  storage: {},
  secrets: {},
  placement: { node_names: [] },
  supabase: {
    public_url: 'https://api.example.test',
    site_url: 'https://app.example.test',
    database_name: 'postgres',
    jwt_expiry_seconds: 3600,
    rest_max_rows: 1000,
    storage_file_limit_bytes: 1048576,
    pool_size: 10,
    pool_max_clients: 100,
    email_signup: false,
    anonymous_signup: false,
  },
} satisfies ManagedPlatformDefaults

test('platform review requires every configured secret at its selected revision', () => {
  const refs = [
    { name: 'auth', revision: 1 },
    { name: 'auth', revision: 2 },
    { name: 'database', revision: 3 },
  ]
  assert.equal(
    completePlatformSpec(draft, ['node-a'], ['auth', 'db'], { auth: 'auth@2' }, refs),
    null,
  )
  assert.equal(completePlatformSpec(draft, ['node-a'], ['auth'], { auth: 'auth@3' }, refs), null)
  const result = completePlatformSpec(
    draft,
    ['node-a'],
    ['auth', 'db'],
    { auth: 'auth@2', db: 'database@3' },
    refs,
  )
  assert.deepEqual(result?.secrets, {
    auth: { name: 'auth', revision: 2 },
    db: { name: 'database', revision: 3 },
  })
  assert.deepEqual(result?.placement, { node_names: ['node-a'], spread: '' })
  assert.deepEqual(draft.placement.node_names, [])
})

test('both guided flows include a final review and paginate a large secret inventory', () => {
  for (const kind of ['supabase', 'neon']) {
    const steps = platformSteps(
      kind,
      Array.from({ length: 27 }, (_, index) => `secret-${index}`),
    )
    assert.equal(steps.at(-1)?.id, 'review')
    assert.equal(new Set(steps.map((step) => step.id)).size, steps.length)
    assert.equal(steps.filter((step) => step.stage === 'Secrets').length, 7)
    assert.ok(steps.some((step) => step.id === 'placement'))
    assert.ok(steps.some((step) => step.id === 'resources'))
  }
})
