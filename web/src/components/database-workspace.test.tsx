import assert from 'node:assert/strict'
import test from 'node:test'
import { renderToStaticMarkup } from 'react-dom/server'
import type { ManagedDatabase } from '../lib/databases'
import { DatabaseHealthLine, databaseState, readyMembers } from './database-workspace'

const now = Date.parse('2026-10-29T12:00:00Z')
const database = (patch: Partial<ManagedDatabase['observation']> & { status?: string } = {}) =>
  ({
    id: 'db1',
    project: 'p',
    environment: 'e',
    revision: 2,
    status: patch.status ?? 'ready',
    spec: { name: 'main', engine: 'postgresql', version: '16', mode: 'cluster', shards: 1, replicas: 2 },
    observation: {
      observed_at: '2026-10-29T11:59:50Z',
      revision: 2,
      status: 'ready',
      message: '',
      members: [{ name: 'm0', uid: 'u0', role: 'primary', ready: true }],
      endpoints: [],
      slots_healthy: true,
      ...patch,
    },
    created_at: '2026-10-01T00:00:00Z',
    updated_at: '2026-10-01T00:00:00Z',
  }) as unknown as ManagedDatabase

test('database state keeps pending, stale and unobserved apart from ready', () => {
  assert.equal(databaseState(database(), now), 'ready')
  assert.equal(databaseState(database({ status: 'pending' }), now), 'pending')
  assert.equal(databaseState(database({ observed_at: '2026-10-29T11:50:00Z' }), now), 'stale')
  assert.equal(databaseState(database({ revision: 1 }), now), 'stale')
  assert.equal(databaseState(database({ observed_at: '0001-01-01T00:00:00Z' }), now), 'not observed')
})

test('ready members are unknown, not zero, before the first observation', () => {
  assert.equal(readyMembers(database(), now), '1 of 3')
  assert.equal(readyMembers(database({ observed_at: '0001-01-01T00:00:00Z', members: [] }), now), undefined)
  const html = renderToStaticMarkup(
    <DatabaseHealthLine database={database({ observed_at: '0001-01-01T00:00:00Z', members: [] })} now={now} />,
  )
  assert.match(html, /none observed/)
  assert.match(html, /Not observed yet/)
  assert.equal(/0 of 3/.test(html), false)
})
