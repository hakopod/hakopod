import assert from 'node:assert/strict'
import test from 'node:test'
import { databaseActivityFresh, databaseHistorySamples, databaseActivityCounter } from './database-monitoring'
import type { ManagedDatabase } from './databases'

test('native Oracle transaction and ClickHouse query counters use their actual API fields', () => {
  const oracle = databaseActivityCounter('oracle', { available: true, transactions: 42 })
  assert.deepEqual(oracle, { label: 'Transactions', value: 42 })
  const clickhouse = databaseActivityCounter('clickhouse', { available: true, commands: 18 })
  assert.deepEqual(clickhouse, { label: 'Server queries', value: 18 })
  assert.equal(databaseActivityCounter('oracle', undefined).value, undefined)
})

test('activity never treats a newly fetched old observation as live', () => {
  const now = Date.parse('2026-09-29T08:00:00Z')
  const at = (offset: number) => new Date(now + offset).toISOString()
  const d = { revision: 1, observation: { revision: 1, observed_at: at(-5000), engine_metrics: { available: true, sampled_at: at(-5000) } } } as ManagedDatabase
  assert.equal(databaseActivityFresh(d, now, now), true)
  assert.equal(databaseActivityFresh({ ...d, observation: { ...d.observation, observed_at: at(-180000), engine_metrics: { available: true, sampled_at: at(-180000) } } }, now, now), false)
  assert.equal(databaseActivityFresh({ ...d, observation: { ...d.observation, observed_at: at(5000) } }, now, now), false)
  assert.equal(databaseActivityFresh({ ...d, observation: { ...d.observation, engine_metrics: { available: true, sampled_at: at(60000) } } }, now, now), false)
  assert.equal(databaseActivityFresh({ ...d, revision: 2 }, now, now), false)
})

test('history preserves gaps, revision boundaries and unavailable samples', () => {
  const point = (minute: number, revision = 1) => ({ revision, status: 'ready', observed_at: `2026-09-29T08:0${minute}:00Z`, resources: { available: true, sampled_at: `2026-09-29T08:0${minute}:00Z`, cpu_millicores: 10, memory_bytes: 100, pods_sampled: 1, pods_expected: 1 } })
  const samples = databaseHistorySamples([point(0), point(1), { ...point(2), resources: undefined }, point(3), point(4, 2), { ...point(5), resources: { ...point(5).resources, cpu_millicores: Infinity } }, point(6, 2)])
  assert.deepEqual(samples.map((sample) => sample.breakBefore), [true, false, true, true, true])
  assert.equal(databaseHistorySamples([{ ...point(1), observed_at: 'invalid' }]).length, 0)
  assert.equal(databaseHistorySamples([{ ...point(1), resources: { ...point(1).resources, sampled_at: point(2).observed_at } }]).length, 0)
})
