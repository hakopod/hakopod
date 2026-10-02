import assert from 'node:assert/strict'
import test from 'node:test'
import { endpointAddress, endpointName, databaseCapacity, databaseMemberCapacity, databaseRequestedCapacity, databaseStorageGiB } from './database-view'
import { databaseCreateIssue, initialDatabaseSpec, databaseEngineDefaults } from './database-create'

test('Vitess remains gated and accounts for tablets and dedicated supporting services', () => {
  const spec = { ...databaseEngineDefaults({ ...initialDatabaseSpec, name: 'orders' }, 'vitess'), vitess: { backup_destination_id: 'a'.repeat(32), backup_destination_revision: 1 } }
  assert.equal(spec.version, '23')
  assert.match(databaseCreateIssue(spec, 0) || '', /available/)
  for (const step of [1, 2, 3]) assert.equal(databaseCreateIssue(spec, step), undefined)
  // 5Gi data, 3Gi topology storage and two 5Gi native-backup volumes.
  assert.equal(databaseStorageGiB(spec), 18)
  assert.ok(Math.abs(databaseRequestedCapacity(spec)!.cpu - 1.55) < 0.000001)
  assert.equal(databaseRequestedCapacity(spec)?.memoryMiB, 2944)
  const cluster = { ...spec, mode: 'cluster' as const, shards: 2, replicas: 1, vitess: { ...spec.vitess, tables: [{ name: 'orders', sharding_column: 'tenant_id' }] } }
  assert.equal(databaseStorageGiB(cluster), 43)
  assert.equal(databaseCreateIssue(cluster, 1), undefined)
  assert.equal(databaseCreateIssue(cluster, 3), undefined)
  assert.match(databaseCreateIssue({ ...cluster, shards: 3 }, 1) || '', /1, 2, 4 or 8/)
  assert.match(databaseCreateIssue({ ...cluster, vitess: { ...cluster.vitess, tables: [] } }, 3) || '', /routing entries/)
  assert.match(databaseCreateIssue({ ...spec, vitess: undefined }, 3) || '', /operator-approved/)
  assert.match(databaseCreateIssue({ ...spec, memory: '512Mi' }, 2) || '', /1Gi/)
  assert.equal(databaseEngineDefaults(spec, 'postgresql').vitess, undefined)
})

test('creation review blocks unavailable engines and unsupported topology', () => {
  const base = { ...initialDatabaseSpec, name: 'orders-db' }
  for (let step = 0; step < 4; step++) assert.equal(databaseCreateIssue(base, step), undefined)
  assert.match(databaseCreateIssue({ ...base, name: 'orders-' }, 0) || '', /Start with a letter/)
  assert.match(databaseCreateIssue({ ...base, engine: 'vitess' as typeof base.engine }, 0) || '', /available/)
  const cluster = { ...base, mode: 'cluster' as const, replicas: 6 }
  assert.equal(databaseCreateIssue(cluster, 1), undefined)
  assert.match(databaseCreateIssue({ ...cluster, replicas: 7 }, 1) || '', /between 1 and 6/)
  assert.match(databaseCreateIssue({ ...cluster, placement: { spread: 'nodes', node_names: ['worker-1'] } }, 1) || '', /7 eligible nodes/)
  assert.match(databaseCreateIssue({ ...cluster, placement: { node_names: ['worker-1', 'worker-1'] } }, 1) || '', /distinct/)
  const redis = { ...cluster, engine: 'redis' as const, version: '8', replicas: 2, shards: 16 }
  assert.equal(databaseCreateIssue(redis, 1), undefined)
  assert.match(databaseCreateIssue({ ...redis, replicas: 3 }, 1) || '', /between 1 and 2/)
})

test('MySQL remains held while its sizing rules stay explicit', () => {
  const mysql = { ...initialDatabaseSpec, name: 'orders-mysql', engine: 'mysql' as const, version: '8.4', cpu: '500m', memory: '1Gi' }
  assert.match(databaseCreateIssue(mysql, 0) || '', /available/)
  for (const step of [1, 2, 3]) assert.equal(databaseCreateIssue(mysql, step), undefined)
  assert.match(databaseCreateIssue({ ...mysql, version: '8.0' }, 0) || '', /available/)
  for (const replicas of [2, 4, 6]) assert.equal(databaseCreateIssue({ ...mysql, mode: 'cluster', replicas }, 1), undefined)
  for (const replicas of [1, 3, 5]) assert.match(databaseCreateIssue({ ...mysql, mode: 'cluster', replicas }, 1) || '', /voting members/)
  assert.match(databaseCreateIssue({ ...mysql, cpu: '499m' }, 2) || '', /at least 500m/)
  assert.match(databaseCreateIssue({ ...mysql, memory: '1023Mi' }, 2) || '', /1Gi/)
  for (const [replicas, cpu, memoryMiB] of [[0, 0.7, 1408], [2, 2, 4096], [6, 4.4, 9216]]) {
    const spec = { ...mysql, mode: replicas ? 'cluster' as const : 'standalone' as const, replicas }
    const capacity = databaseRequestedCapacity(spec)!
    assert.ok(Math.abs(capacity.cpu - cpu) < 1e-9)
    assert.equal(capacity.memoryMiB, memoryMiB)
    assert.equal(databaseMemberCapacity(spec)?.memoryMiB, (1 + replicas) * 1280)
  }
})

test('creation review validates resource bounds and requires native TLS', () => {
  const base = { ...initialDatabaseSpec, name: 'orders-db' }
  for (const cpu of ['99m', '17', 'invalid']) assert.match(databaseCreateIssue({ ...base, cpu }, 2) || '', /CPU/)
  for (const memory of ['127Mi', '65Gi', '2GB']) assert.match(databaseCreateIssue({ ...base, memory }, 2) || '', /Memory/)
  assert.match(databaseCreateIssue({ ...base, storage_gib: 1.5 }, 2) || '', /Storage/)
  assert.match(databaseCreateIssue({ ...base, tls: undefined }, 3) || '', /TLS/)
})

test('MongoDB allocation includes agents and log volumes without introducing routers', () => {
  const mongo = { ...initialDatabaseSpec, name: 'events', engine: 'mongodb' as const, version: '8.0', cpu: '500m', memory: '1Gi' }
  for (const replicas of [0, 2, 4, 6]) {
    const spec = { ...mongo, mode: replicas ? 'cluster' as const : 'standalone' as const, replicas }
    for (let step = 0; step < 4; step++) assert.equal(databaseCreateIssue(spec, step), undefined)
    const capacity = databaseRequestedCapacity(spec)!
    assert.ok(Math.abs(capacity.cpu - (replicas + 1) * 0.6) < 1e-9)
    assert.equal(capacity.memoryMiB, (replicas + 1) * 1280)
    assert.equal(databaseStorageGiB(spec), (replicas + 1) * 6)
  }
  assert.match(databaseCreateIssue({ ...mongo, mode: 'cluster', replicas: 3 }, 1) || '', /voting members/)
  assert.match(databaseCreateIssue({ ...mongo, version: '7.0' }, 0) || '', /supported/)
  assert.match(databaseCreateIssue({ ...mongo, memory: '512Mi' }, 2) || '', /1Gi/)
})


test('pooler capacity is included in creation without inflating member monitoring limits', () => {
  const spec = { ...initialDatabaseSpec, name: 'orders-db', mode: 'cluster' as const, replicas: 1, pooling: { mode: 'transaction' as const, instances: 2, max_client_connections: 200, default_pool_size: 10, read_only: true } }
  assert.equal(databaseCreateIssue(spec, 3), undefined)
  assert.equal(databaseRequestedCapacity(spec)?.cpu, 1.5)
  assert.equal(databaseRequestedCapacity(spec)?.memoryMiB, 2048)
  assert.equal(databaseCapacity(spec)?.cpu, 0.5)
  assert.match(databaseCreateIssue({ ...spec, replicas: 0 }, 3) || '', /needs at least one replica/)
  assert.match(databaseCreateIssue({ ...spec, pooling: { ...spec.pooling, default_pool_size: 21 } }, 3) || '', /server connections/)
})

test('ClickHouse remains held while capacity accounts for Keepers and backup staging', () => {
  const base = databaseEngineDefaults({ ...initialDatabaseSpec, name: 'events' }, 'clickhouse')
  assert.match(databaseCreateIssue(base, 0) || '', /available/)
  for (const step of [1, 2, 3]) assert.equal(databaseCreateIssue(base, step), undefined)
  const spec = { ...base, mode: 'cluster' as const, shards: 2, replicas: 2 }
  assert.equal(databaseRequestedCapacity(spec)?.cpu, 3.75)
  assert.equal(databaseRequestedCapacity(spec)?.memoryMiB, 13056)
  assert.equal(databaseStorageGiB(spec), 63)
  assert.equal(databaseMemberCapacity(spec)?.memoryMiB, 12288)
  assert.match(databaseCreateIssue({ ...spec, replicas: 6 }, 1) || '', /between 1 and 5/)
  assert.match(databaseCreateIssue({ ...spec, shards: 9 }, 1) || '', /1–8 shards/)
  assert.match(databaseCreateIssue({ ...spec, memory: '1Gi' }, 2) || '', /2Gi/)
  assert.match(databaseCreateIssue({ ...spec, shards: 1, replicas: 1, placement: { spread: 'nodes', node_names: ['one', 'two'] } }, 1) || '', /3 eligible nodes/)
})

test('Oracle remains held while its defaults reserve recovery storage', () => {
  const previous = { ...initialDatabaseSpec, name: 'orders', mode: 'cluster' as const, engine: 'redis' as const, replicas: 2, shards: 3, placement: { spread: 'nodes' as const } }
  const oracle = databaseEngineDefaults(previous, 'oracle')
  assert.match(databaseCreateIssue(oracle, 0) || '', /available/)
  for (const step of [1, 2, 3]) assert.equal(databaseCreateIssue(oracle, step), undefined)
  assert.equal(oracle.mode, 'standalone')
  assert.equal(oracle.oracle?.edition, 'free')
  assert.equal(databaseStorageGiB(oracle), 20)
  assert.equal(databaseRequestedCapacity(oracle)?.memoryMiB, 4096)
  assert.match(databaseCreateIssue({ ...oracle, mode: 'cluster', replicas: 1 }, 1) || '', /Free supports standalone/)
  assert.match(databaseCreateIssue({ ...oracle, oracle: { edition: 'enterprise', image: 'registry.example/oracle:latest' } }, 0) || '', /available/)
  assert.match(databaseCreateIssue({ ...oracle, memory: '2Gi' }, 2) || '', /4Gi/)
  assert.equal(databaseEngineDefaults(oracle, 'postgresql').oracle, undefined)
})

test('Vitess copied endpoints retain the tablet routing target', () => {
  const endpoint = { host: 'database.hdb-fixture.svc', port: 3306, purpose: 'read_only' }
  assert.equal(endpointName(endpoint.purpose, 'vitess'), 'vtgate · app@replica')
  assert.equal(endpointAddress(endpoint, 'vitess'), 'database.hdb-fixture.svc:3306/app@replica')
  assert.equal(endpointAddress({ ...endpoint, purpose: 'read_write' }, 'vitess'), 'database.hdb-fixture.svc:3306/app@primary')
  assert.equal(endpointAddress(endpoint, 'mysql'), 'database.hdb-fixture.svc:3306')
})
