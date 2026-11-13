import assert from 'node:assert/strict'
import test from 'node:test'
import { renderToStaticMarkup } from 'react-dom/server'
import {
  applicationNameFrom,
  defaultEnvironment,
  filledValues,
  migrationActive,
  stillMissing,
  type PlatformMigration,
} from '../lib/platform-migrations'
import { MigrationProgress, MigrationReview } from './platform-migration'

const migration = (patch: Partial<PlatformMigration> = {}): PlatformMigration => ({
  id: 'a'.repeat(32),
  project: 'shop',
  environment: 'production',
  application_name: 'storefront',
  provider: 'railway',
  source_project_id: 'railway-project',
  source_project_name: 'Storefront',
  status: 'planned',
  steps: [],
  cancel_requested: false,
  has_token: true,
  created_at: '2026-11-13T10:00:00Z',
  expires_at: '2026-11-14T10:00:00Z',
  proposal: {
    spec: {
      schema_version: 1,
      name: 'storefront',
      services: {
        web: {
          image: 'docker.io/library/busybox@sha256:00',
          port: 3000,
          public: true,
          replicas: 2,
          secrets: { STRIPE_SECRET_KEY: 'migrate-0' },
        },
        nightly: { image: 'ghcr.io/acme/jobs:1', job: { schedule: { cron: '0 3 * * *' } } },
      } as never,
    },
    builds: [
      { service: 'web', repository: 'acme/storefront', branch: 'main', connection_id: 'gh' },
    ],
    databases: [
      { name: 'postgres', engine: 'postgresql', version: '16', copy_data: true },
      { name: 'mongo', engine: 'mongodb', copy_data: false, reason: 'Copy MongoDB data yourself.' },
    ],
    secret_names: ['migrate-0'],
    domains: { 'shop.example.com': 'web' },
    missing: [{ service: 'web', name: 'SESSION_SECRET' }],
    warnings: ['Service web keeps a volume, so it runs one replica instead of 2.'],
  },
  ...patch,
})

test('migration helpers name the application, pick production and track missing values', () => {
  assert.equal(applicationNameFrom('My Shop API!'), 'my-shop-api')
  assert.equal(applicationNameFrom('2024 Shop'), 'shop')
  assert.equal(
    defaultEnvironment({
      id: 'p',
      name: 'p',
      environments: [
        { id: 'staging-id', name: 'staging' },
        { id: 'production-id', name: 'production' },
      ],
    }),
    'production-id',
  )
  assert.equal(defaultEnvironment({ id: 'p', name: 'p' }), undefined)
  const proposal = migration().proposal
  assert.equal(stillMissing(proposal).length, 1)
  assert.equal(stillMissing(proposal, { web: { SESSION_SECRET: 'x' } }).length, 0)
  assert.deepEqual(filledValues({ web: { A: 'x', B: '' }, api: { C: '' } }), { web: { A: 'x' } })
  assert.equal(migrationActive('running'), true)
  assert.equal(migrationActive('planned'), false)
})

test('migration review shows sources, data, secret names, pending domains and missing inputs', () => {
  const html = renderToStaticMarkup(
    <MigrationReview
      migration={migration()}
      values={{}}
      onValue={() => {}}
      onSave={() => {}}
      saving={false}
    />,
  )
  assert.match(html, /GitHub acme\/storefront@main/)
  assert.match(html, /Port 3000 · 2 replicas/)
  assert.match(html, /<code>0 3 \* \* \*<\/code>/)
  assert.match(html, /Data copied/)
  assert.match(html, /Copy MongoDB data yourself\./)
  assert.match(html, /STRIPE_SECRET_KEY/)
  assert.doesNotMatch(html, /migrate-0/)
  assert.match(html, /Pending DNS/)
  assert.match(html, /keeps a volume/)
  assert.match(html, /type="password"[^>]*value=""/)
  assert.match(html, /<button[^>]*disabled=""[^>]*>Save values/)
})

test('migration progress names each step and keeps created resources on failure', () => {
  const html = renderToStaticMarkup(
    <MigrationProgress
      migration={migration({
        status: 'failed',
        error: 'Database postgres did not become ready.',
        steps: [
          { name: 'secrets', status: 'succeeded', items: [{ name: 'web', status: 'succeeded' }] },
          { name: 'databases', status: 'failed', message: 'postgres timed out' },
          { name: 'data', status: 'pending' },
        ],
      })}
    />,
  )
  assert.match(html, /Migration failed/)
  assert.match(html, /role="alert"[^>]*>Database postgres did not become ready\./)
  assert.match(html, /already created on Hakopod was kept/)
  assert.match(html, /Data copy/)
  assert.match(html, /postgres timed out/)
})
