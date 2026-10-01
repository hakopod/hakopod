import assert from 'node:assert/strict'
import test from 'node:test'
import { databaseMetricAge, expectedMembers, metricAvailable, topologyGroups } from './database-view'
import { oracleSwitchoverNeedsFreshReview, type DatabaseOperation, type ManagedDatabase } from './databases'

test('Oracle fresh review is limited to the current database pre-submission failure', () => {
  // Synthetic records exercise the UI recovery boundary, not an Oracle runtime.
  const d = { id: 'fixture-oracle', revision: 3, status: 'failed' } as ManagedDatabase
  const staleReview = { database_id: d.id, revision: 3, kind: 'switchover', status: 'failed', phase: 'review' } as DatabaseOperation
  assert.equal(oracleSwitchoverNeedsFreshReview(d, staleReview), true)
  assert.equal(oracleSwitchoverNeedsFreshReview(d), false)
  assert.equal(oracleSwitchoverNeedsFreshReview({ ...d, status: 'pending' }, staleReview), false)
  for (const change of [
    { database_id: 'another-database' }, { revision: 2 }, { revision: 4 },
    { kind: 'create' }, { kind: 'resize' }, { status: 'running' }, { status: 'succeeded' },
    { phase: 'switchover' }, { phase: 'switchover-verifying' }, { phase: 'switching-primary' },
  ]) {
    assert.equal(oracleSwitchoverNeedsFreshReview(d, { ...staleReview, ...change }), false,
      `must not turn ${JSON.stringify(change)} into a fresh-review permission`)
  }
})

test('database monitoring rejects stale observations even when cached timestamps match', () => {
  const now = Date.parse('2026-09-29T08:00:00Z')
  const fresh = new Date(now - 5000).toISOString()
  const old = new Date(now - 180000).toISOString()
  const sample = {
    available: true,
    cpu_millicores: 0,
    memory_bytes: 0,
    sampled_at: fresh,
    pods_sampled: 1,
    pods_expected: 1,
  }
  assert.equal(metricAvailable(sample, fresh, now, now), true)
  assert.equal(databaseMetricAge(old, old, now, now), 180000)
  assert.equal(databaseMetricAge(old, old, now, now + 10000), 190000)
  assert.equal(databaseMetricAge(fresh, fresh, now, now), 5000)
  assert.equal(metricAvailable({ ...sample, pods_sampled: 0 }, fresh, now, now), false)
  assert.equal(metricAvailable({ ...sample, pods_expected: 0, pods_sampled: 0 }, fresh, now, now), false)
  assert.equal(metricAvailable({ ...sample, sampled_at: old }, old, now, now), false)
  assert.equal(metricAvailable({ ...sample, sampled_at: old }, fresh, now, now), false)
  assert.equal(metricAvailable({ ...sample, available: false }, fresh, now, now), false)
  assert.equal(metricAvailable({ ...sample, cpu_millicores: -1 }, fresh, now, now), false)
  assert.equal(metricAvailable({ ...sample, memory_bytes: undefined }, fresh, now, now), false)
  assert.equal(metricAvailable(undefined, fresh, now, now), false)
})

test('Redis topology groups only observed shard identities and retains unknown members', () => {
  const d = {
    spec: { engine: 'redis', mode: 'cluster', shards: 3, replicas: 1 },
    observation: {
      members: [
        { name: 'p2', uid: 'p2', role: 'primary', shard: 'b' },
        { name: 'r1', uid: 'r1', role: 'replica', shard: 'a' },
        { name: 'p1', uid: 'p1', role: 'primary', shard: 'a' },
        { name: 'unknown', uid: 'unknown', role: 'unknown' },
      ],
    },
  } as ManagedDatabase
  assert.equal(expectedMembers(d.spec), 6)
  const groups = topologyGroups(d)
  assert.deepEqual(
    groups.map((g) => [g.id, g.members.map((m) => m.uid)]),
    [
      ['a', ['r1', 'p1']],
      ['b', ['p2']],
      ['unknown', ['unknown']],
    ],
  )
  assert.deepEqual(topologyGroups({ ...d, observation: { ...d.observation, members: [] } }), [])
})
