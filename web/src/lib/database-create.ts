import type { DatabaseSpec } from './databases'
import { databaseCapacity, expectedMembers, engineName, votingDatabase, placementDomains } from './database-view'

export const databaseCreateSteps = ['Engine', 'Topology', 'Resources', 'Security', 'Review'] as const
export const initialDatabaseSpec: DatabaseSpec = {
  schema_version: 1, name: '', engine: 'postgresql', version: '17', mode: 'standalone',
  replicas: 0, shards: 1, cpu: '250m', memory: '512Mi', storage_gib: 5, tls: { mode: 'required' },
}

// This is a presentation catalog, not a claim of backend engine support.
export const databaseEngines = [
  { id: 'postgresql', name: 'PostgreSQL', category: 'Relational', description: 'SQL, transactions and a rich extension ecosystem.', enabled: true },
  { id: 'redis', name: 'Redis', category: 'Key-value', description: 'In-memory data, caching, queues and streams.', enabled: true },
  { id: 'mysql', name: 'MySQL', category: 'Relational', description: 'SQL transactions with InnoDB Cluster and explicit read/write routes.', enabled: true },
  { id: 'mongodb', name: 'MongoDB', category: 'Document', description: 'Flexible documents and native replica sets.', enabled: true },
  { id: 'clickhouse', name: 'ClickHouse', category: 'Analytics', description: 'Column-oriented storage for analytics and replicated shards.', enabled: true },
  { id: 'oracle', name: 'Oracle Database', category: 'Relational', description: 'Oracle Database Free with private TLS connections and schema backups.', enabled: true },
  { id: 'vitess', name: 'Vitess', category: 'Distributed MySQL', description: 'MySQL-compatible routing across managed shards.', enabled: true },
  { id: 'duckdb', name: 'DuckDB (MyDuck)', category: 'Analytics', description: 'One DuckDB instance, accessible through MySQL and PostgreSQL clients.', enabled: true },
] as const

export const databaseVersions = (engine: string) => ({ mongodb: ['8.0'], mysql: ['8.4'], redis: ['8'], clickhouse: ['26.3'], oracle: ['23.26'], vitess: ['23'], duckdb: ['0.3.1-dev.20260919.3'], postgresql: ['17', '18'] })[engine] || []
export const databaseMinimum = (engine: string) => engine === 'oracle' ? { cpu: 1, memoryMiB: 4096, storageGiB: 10 } : engine === 'clickhouse' ? { cpu: 0.5, memoryMiB: 2048, storageGiB: 1 } : engine === 'duckdb' ? { cpu: 0.1, memoryMiB: 512, storageGiB: 1 } : (votingDatabase(engine) || engine === 'vitess') ? { cpu: 0.5, memoryMiB: 1024, storageGiB: 1 } : { cpu: 0.1, memoryMiB: 128, storageGiB: 1 }
export const maximumReplicas = (engine: string) => engine === 'redis' ? 2 : ['clickhouse', 'vitess'].includes(engine) ? 5 : 6

export function databaseEngineDefaults(spec: DatabaseSpec, engine: DatabaseSpec['engine']): DatabaseSpec {
  const minimum = databaseMinimum(engine), capacity = databaseCapacity(spec, 1)
  const mode = engine === 'oracle' || engine === 'duckdb' ? 'standalone' : spec.mode
  return {
    ...spec, engine, mode, version: databaseVersions(engine)[0], pooling: undefined,
    oracle: engine === 'oracle' ? { edition: 'free' } : undefined,
    vitess: engine === 'vitess' ? spec.vitess || { tables: [], backup_destination_id: '', backup_destination_revision: 0 } : undefined,
    shards: engine === 'redis' && mode === 'cluster' ? 3 : 1,
    replicas: mode === 'cluster' ? votingDatabase(engine) ? 2 : Math.max(1, Math.min(spec.replicas, maximumReplicas(engine))) : 0,
    placement: { ...spec.placement, spread: mode === 'standalone' ? '' : spec.placement?.spread },
    cpu: (capacity?.cpu || 0) < minimum.cpu ? String(minimum.cpu) : spec.cpu,
    memory: (capacity?.memoryMiB || 0) < minimum.memoryMiB ? `${minimum.memoryMiB}Mi` : spec.memory,
    storage_gib: Math.max(spec.storage_gib, minimum.storageGiB),
  }
}

export function databaseCreateIssue(spec: DatabaseSpec, step: number): string | undefined {
  if (step === 0) {
    if (!databaseEngines.some((engine) => engine.enabled && engine.id === spec.engine)) return 'Choose an available database engine.'
    if (!/^(?:[a-z]|[a-z][a-z0-9-]{0,38}[a-z0-9])$/.test(spec.name)) return 'Use 1–40 lowercase letters, numbers or hyphens. Start with a letter and end with a letter or number.'
    if (!databaseVersions(spec.engine).includes(spec.version)) return 'Choose a supported database version.'
    if (spec.engine === 'oracle' && (spec.oracle?.edition !== 'free' || spec.oracle.image || spec.oracle.registry_credential || spec.oracle.license_confirmed)) return 'Choose Oracle Database Free. Customer-licensed Enterprise deployments are still in development.'
    if (spec.engine !== 'oracle' && spec.oracle) return 'Oracle edition settings only apply to Oracle Database.'
  }
  if (step === 1) {
    if (!['standalone', 'cluster'].includes(spec.mode)) return 'Choose a deployment layout.'
    if (spec.engine === 'oracle' && spec.mode !== 'standalone') return 'Oracle Database Free supports standalone deployments. Data Guard requires Enterprise edition.'
    if (spec.engine === 'duckdb' && spec.mode !== 'standalone') return 'DuckDB (MyDuck) currently supports one standalone instance.'
    if (spec.mode === 'cluster' && (!Number.isInteger(spec.replicas) || spec.replicas < 1 || spec.replicas > maximumReplicas(spec.engine))) return `Choose between 1 and ${maximumReplicas(spec.engine)} replicas.`
    const [minShards, maxShards] = spec.mode === 'cluster' && spec.engine === 'redis' ? [3, 16] : spec.mode === 'cluster' && ['clickhouse', 'vitess'].includes(spec.engine) ? [1, 8] : [1, 1]
    if (!Number.isInteger(spec.shards) || spec.shards < minShards || spec.shards > maxShards) return `Choose ${minShards === maxShards ? 'one shard' : `${minShards}–${maxShards} shards`} for this layout.`
    if (spec.engine === 'vitess' && ![1, 2, 4, 8].includes(spec.shards)) return 'Vitess supports 1, 2, 4 or 8 shards.'
    if (spec.mode === 'standalone' && (spec.replicas !== 0 || spec.placement?.spread)) return 'Standalone databases have one member and no placement separation.'
    if (votingDatabase(spec.engine) && spec.mode === 'cluster' && ![2, 4, 6].includes(spec.replicas)) return `${engineName(spec.engine)} clusters need 3, 5 or 7 voting members: one primary and 2, 4 or 6 replicas.`
    if (expectedMembers(spec) > 48) return 'A database can have at most 48 members. Reduce the number of shards or replicas.'
    const nodes = spec.placement?.node_names || []
    if (nodes.length > 48 || new Set(nodes).size !== nodes.length || nodes.some((node) => node.length > 253 || !/^[a-z0-9](?:[-a-z0-9]*[a-z0-9])?(?:\.[a-z0-9](?:[-a-z0-9]*[a-z0-9])?)*$/.test(node))) return 'Enter up to 48 distinct, valid node names.'
    if (spec.placement?.spread && nodes.length && nodes.length < placementDomains(spec)) return `Strict separation needs at least ${placementDomains(spec)} eligible nodes.`
  }
  if (step === 2) {
    const cpu = databaseCapacity({ ...spec, memory: '128Mi' }, 1)
    const memory = databaseCapacity({ ...spec, cpu: '1' }, 1)
    if (!cpu || cpu.cpu < 0.1 || cpu.cpu > 16) return 'CPU must be between 100m and 16 cores per member.'
    if (!memory || memory.memoryMiB < 128 || memory.memoryMiB > 65536) return 'Memory must be between 128Mi and 64Gi per member.'
    if ((votingDatabase(spec.engine) || spec.engine === 'vitess') && (cpu!.cpu < 0.5 || memory!.memoryMiB < 1024)) return `${engineName(spec.engine)} requires at least 500m CPU and 1Gi memory per database member.`
    if (spec.engine === 'duckdb' && memory!.memoryMiB < 512) return 'DuckDB (MyDuck) requires at least 512Mi memory.'
    if (spec.engine === 'clickhouse' && (cpu!.cpu < 0.5 || memory!.memoryMiB < 2048)) return 'ClickHouse requires at least 500m CPU and 2Gi memory per data member.'
    if (spec.engine === 'oracle' && (cpu!.cpu < 1 || memory!.memoryMiB < 4096 || spec.storage_gib < 10)) return 'Oracle Database Free requires at least 1 CPU, 4Gi memory and 10Gi storage, plus backup staging storage.'
    if (!Number.isInteger(spec.storage_gib) || spec.storage_gib < 1 || spec.storage_gib > 1024) return 'Storage must be between 1 and 1024 GiB per member.'
  }
  if (step === 3) {
    if (spec.tls?.mode !== 'required') return 'New databases require verified TLS connections.'
    if (spec.engine === 'vitess') {
      const config = spec.vitess
      if (!config || !/^[a-f0-9]{32}$/.test(config.backup_destination_id) || !Number.isInteger(config.backup_destination_revision) || config.backup_destination_revision < 1) return 'Vitess requires a dedicated operator-approved native backup destination and its revision.'
      const tables = config.tables || []
      if (spec.shards > 1 ? tables.length < 1 || tables.length > 128 : tables.length > 0) return 'Use 1–128 table routing entries for sharded Vitess and none for a single shard.'
      if (new Set(tables.map((table) => table.name)).size !== tables.length || tables.some((table) => !/^[A-Za-z_][A-Za-z0-9_]{0,63}$/.test(table.name) || !/^[A-Za-z_][A-Za-z0-9_]{0,63}$/.test(table.sharding_column))) return 'Use distinct table names and valid SQL identifiers for each sharding column.'
    }
    const pool = spec.pooling
    if (pool) {
      if (spec.engine !== 'postgresql') return 'Managed connection pooling requires PostgreSQL.'
      if (!['session', 'transaction'].includes(pool.mode)) return 'Choose a pooling mode.'
      if (!Number.isInteger(pool.instances) || pool.instances < 1 || pool.instances > 3) return 'Choose 1–3 pooler instances per route.'
      if (!Number.isInteger(pool.max_client_connections) || pool.max_client_connections < 20 || pool.max_client_connections > 2000) return 'Choose 20–2000 client connections per pooler.'
      if (!Number.isInteger(pool.default_pool_size) || pool.default_pool_size < 1 || pool.default_pool_size > 20) return 'Choose 1–20 server connections per pooler.'
      if (pool.read_only && spec.replicas < 1) return 'A pooled read endpoint needs at least one replica.'
      const domains = Math.max(expectedMembers(spec), pool.instances)
      if (spec.placement?.spread && spec.placement.node_names?.length && spec.placement.node_names.length < domains) return `Strict separation needs at least ${domains} eligible nodes, including the poolers.`
    }
  }
}
