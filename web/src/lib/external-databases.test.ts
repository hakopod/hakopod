import assert from 'node:assert/strict'
import test from 'node:test'
import { externalDatabaseHealth, externalDatabaseIssue, type ExternalDatabase, type ExternalDatabaseSpec } from './external-databases'

const spec: ExternalDatabaseSpec = { schema_version: 1, name: 'reporting', provider: 'planetscale', engine: 'mysql', host: 'fixture.psdb.cloud', port: 3306, database: 'app' }
const now = Date.parse('2026-09-30T10:00:00Z')
// Development fixture only; these checks do not contact PlanetScale.
const database = { id: 'a'.repeat(32), project: 'fixture', environment: 'development', revision: 3, credential_revision: 2, spec, status: 'ready', created_at: '', updated_at: '', observation: { revision: 3, observed_at: new Date(now).toISOString(), status: 'ready', message: '', tls_verified: true, query_verified: true } } satisfies ExternalDatabase

test('external readiness needs current revision, TLS and authenticated query evidence', () => {
  assert.equal(externalDatabaseHealth(database, now), 'ready')
  assert.equal(externalDatabaseHealth({ ...database, observation: { ...database.observation, revision: 2 } }, now), 'Observation stale')
  assert.notEqual(externalDatabaseHealth({ ...database, observation: { ...database.observation, tls_verified: false } }, now), 'ready')
  assert.notEqual(externalDatabaseHealth({ ...database, observation: { ...database.observation, query_verified: false } }, now), 'ready')
  assert.equal(externalDatabaseHealth(database, now + 120001), 'Observation stale')
  assert.equal(externalDatabaseHealth(database, now - 5001), 'Observation stale')
  assert.equal(externalDatabaseHealth({ ...database, status: 'deleting' }, now), 'deleting')
})
test('external endpoint input rejects lookalike domains, URLs and unsupported native ports', () => {
  assert.equal(externalDatabaseIssue(spec), undefined)
  for (const host of ['psdb.cloud', 'fixture.psdb.cloud.attacker.invalid', 'https://fixture.psdb.cloud', 'fixture.psdb.cloud:3306', '-fixture.psdb.io', 'FIXTURE.psdb.cloud', 'fixture.psdb.cloud.'])
    assert.ok(externalDatabaseIssue({ ...spec, host }), host)
  assert.ok(externalDatabaseIssue({ ...spec, port: 443 }))
  assert.equal(externalDatabaseIssue({ ...spec, engine: 'postgresql', port: 6432 }), undefined)
  assert.ok(externalDatabaseIssue(spec, { username: 'app', password: '' }))
  assert.ok(externalDatabaseIssue(spec, { username: 'app\n', password: 'fixture' }))
})
