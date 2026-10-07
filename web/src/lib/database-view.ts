import type { ManagedDatabase, DatabaseSpec } from './databases'
import { runnerReservation } from './runner-resources'
import { metricSampleAge, metricsStaleAfter } from './runtime-metrics'

export const expectedMembers = (spec: DatabaseSpec) => spec.shards * (1 + spec.replicas)
export const databaseCapacity = (spec: DatabaseSpec, members = expectedMembers(spec)) =>
  runnerReservation({ cpu_request: spec.cpu, memory_request: spec.memory }, members)
export const poolerInstances = (spec: DatabaseSpec) => spec.pooling ? spec.pooling.instances * (spec.pooling.read_only ? 2 : 1) : 0
export const routerInstances = (spec: DatabaseSpec) => spec.engine === 'mysql' ? spec.mode === 'standalone' ? 1 : 2 : 0
export const keeperInstances = (spec: DatabaseSpec) => spec.engine === 'clickhouse' && spec.mode === 'cluster' ? 3 : 0
export const vitessGateways = (spec: DatabaseSpec) => spec.engine === 'vitess' ? spec.mode === 'standalone' ? 1 : 2 : 0
export const vitessTopologyMembers = (spec: DatabaseSpec) => spec.engine === 'vitess' ? 3 : 0
export const oracleBrokerInstances = (spec: DatabaseSpec) => spec.engine === 'oracle' && spec.oracle?.edition === 'enterprise' && spec.mode === 'cluster' ? 1 : 0
export const placementDomains = (spec: DatabaseSpec) => Math.max(expectedMembers(spec), spec.pooling?.instances || 0, keeperInstances(spec), vitessTopologyMembers(spec))
export const shardedDatabase = (engine: string) => ['redis', 'clickhouse', 'vitess'].includes(engine)
export const votingDatabase = (engine: string) => engine === 'mysql' || engine === 'mongodb'
export const databaseStorageGiB = (spec: DatabaseSpec) => (spec.storage_gib * (spec.engine === 'oracle' && spec.oracle?.edition === 'enterprise' ? 3 : ['clickhouse', 'oracle'].includes(spec.engine) ? 2 : 1) + (spec.engine === 'mongodb' ? 1 : 0)) * expectedMembers(spec) + keeperInstances(spec) + vitessTopologyMembers(spec) + (spec.engine === 'vitess' ? 2 * spec.shards * spec.storage_gib : 0)
export const oracleFreeQuotaGiB = (storageGiB: number) => Math.max(0, Math.min(10, storageGiB - 8))
export const databaseLayoutSummary = (spec: DatabaseSpec) => spec.engine === 'vitess' ? `${spec.shards} ${spec.shards === 1 ? 'shard' : 'shards'} · ${expectedMembers(spec)} ${expectedMembers(spec) === 1 ? 'tablet' : 'tablets'} · ${vitessGateways(spec)} ${vitessGateways(spec) === 1 ? 'gateway' : 'gateways'}` : spec.mode === 'standalone' ? 'Standalone · 1 member' : spec.engine === 'clickhouse' ? `${spec.shards} shards · ${spec.replicas + 1} copies per shard · 3 Keeper members` : spec.engine === 'redis' ? `${spec.shards} shards · ${spec.replicas} replicas per shard` : `1 primary · ${spec.replicas} ${spec.replicas === 1 ? 'replica' : 'replicas'}`
export function databaseMemberCapacity(spec: DatabaseSpec) {
  const capacity = databaseCapacity(spec)
  return capacity && (votingDatabase(spec.engine) || spec.engine === 'vitess') ? { ...capacity, cpu: capacity.cpu + expectedMembers(spec) * 0.1, memoryMiB: capacity.memoryMiB + expectedMembers(spec) * 256 } : capacity
}
export function databaseRequestedCapacity(spec: DatabaseSpec) {
  const member = databaseCapacity(spec, 1)
  if (!member) return null
  // Match Go's CPUReservationMilli and DatabaseMemoryReservation. Member
  // monitoring uses databaseMemberCapacity and excludes operational headroom.
  const copies = expectedMembers(spec) + 1
  let cpu = member.cpu * copies
  let memoryMiB = (member.memoryMiB + 50) * copies + 128
  const poolers = poolerInstances(spec)
  if (poolers) {
    const routes = spec.pooling?.read_only ? 2 : 1
    cpu += (poolers + routes) * 0.25
    memoryMiB += (poolers + routes) * 306
  }
  if (votingDatabase(spec.engine)) {
    cpu += copies * 0.1
    memoryMiB += copies * 256
  }
  if (spec.engine === 'mysql') {
    cpu += (routerInstances(spec) + 1) * 0.1
    memoryMiB += (routerInstances(spec) + 1) * 178
  }
  const keepers = keeperInstances(spec)
  if (keepers) {
    cpu += (keepers + 1) * 0.25
    memoryMiB += (keepers + 1) * 306
  }
  if (spec.engine === 'vitess') {
    cpu += copies * 0.1 + (vitessGateways(spec) + 1) * 0.25 +
      (spec.shards + 2) * 0.5 + (vitessTopologyMembers(spec) + 1) * 0.1 +
      2 * 0.1 + 0.1 + 2 * spec.shards * (member.cpu + 0.1)
    memoryMiB += copies * 256 + (vitessGateways(spec) + 1) * 306 +
      (spec.shards + 2) * 306 + (vitessTopologyMembers(spec) + 1) * 306 +
      2 * 306 + 562 + 2 * spec.shards * (member.memoryMiB + 512 + 50)
  }
  if (spec.engine === 'oracle' && spec.oracle?.edition === 'free') {
    cpu += 0.2
    memoryMiB += 2 * 306
  }
  cpu += oracleBrokerInstances(spec) * 0.1
  memoryMiB += oracleBrokerInstances(spec) * 306
  return { cpu, memoryMiB }
}
export const engineName = (engine: string) => ({ postgresql: 'PostgreSQL', redis: 'Redis', mysql: 'MySQL', mongodb: 'MongoDB', clickhouse: 'ClickHouse', oracle: 'Oracle Database', vitess: 'Vitess', duckdb: 'DuckDB (MyDuck)' })[engine] || engine
export const endpointName = (purpose: string, engine?: string) => engine === 'duckdb' && purpose === 'mysql' ? 'MySQL protocol · port 3306' : engine === 'duckdb' && purpose === 'postgresql' ? 'PostgreSQL protocol · port 5432' : engine === 'vitess' && ['read_write', 'read_only'].includes(purpose) ? purpose === 'read_only' ? 'vtgate · app@replica' : 'vtgate · app@primary' : engine === 'clickhouse' && purpose === 'cluster' ? 'Cluster endpoint' :
  ({ read_write: 'Read / write', read_only: 'Read only', cluster: 'Cluster discovery', pooled_read_write: 'Pooled write', pooled_read_only: 'Pooled read' })[purpose] ||
  purpose.replaceAll('_', ' ')
export const endpointAddress = (endpoint: { host: string; port: number; purpose: string }, engine: string) => `${endpoint.host}:${endpoint.port}${engine === 'vitess' ? endpoint.purpose === 'read_only' ? '/app@replica' : '/app@primary' : ''}`

export function databaseMetricAge(sampledAt: string | undefined, observedAt: string, receivedAt: number, now: number) {
  const age = metricSampleAge(sampledAt, observedAt, receivedAt, now)
  const observed = Date.parse(observedAt)
  if (age === null || !Number.isFinite(observed) || observed > now) return null
  return age + Math.max(0, receivedAt - observed)
}

export function metricAvailable(
  metrics: ManagedDatabase['observation']['metrics'],
  observedAt: string,
  receivedAt: number,
  now: number,
) {
  const age = databaseMetricAge(metrics?.sampled_at, observedAt, receivedAt, now)
  return Boolean(
    metrics?.available &&
    Number.isInteger(metrics.pods_expected) && metrics.pods_expected > 0 &&
    metrics.pods_sampled === metrics.pods_expected &&
    now - Date.parse(observedAt) <= 30_000 &&
    Date.parse(observedAt) <= now &&
    age !== null &&
    age <= metricsStaleAfter &&
    (metrics.cpu_millicores ?? -1) >= 0 &&
    (metrics.memory_bytes ?? -1) >= 0 &&
    Number.isFinite(metrics.cpu_millicores) &&
    Number.isFinite(metrics.memory_bytes),
  )
}
export function topologyGroups(d: ManagedDatabase) {
  const members = d.observation.members || []
  if (!shardedDatabase(d.spec.engine) || d.spec.mode !== 'cluster')
    return [{ id: 'database', label: 'Database', members }]
  const ids = [
    ...new Set(members.map((m) => m.shard).filter((id): id is string => Boolean(id))),
  ].sort()
  const groups = ids.map((id) => ({
    id,
    label: `Shard ${id.slice(0, 10)}`,
    members: members.filter((m) => m.shard === id),
  }))
  const unknown = members.filter((m) => !m.shard)
  if (unknown.length) groups.push({ id: 'unknown', label: 'Unassigned', members: unknown })
  return groups
}
