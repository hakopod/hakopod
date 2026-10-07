export { DatabaseTopology } from './database-topology'
import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { client, unwrap } from '../lib/client'
import { SelectField } from './ui/select'
import type { ManagedDatabase } from '../lib/databases'
import { databaseHealth } from '../lib/databases'
import {
  databaseMemberCapacity,
  databaseStorageGiB,
  databaseLayoutSummary,
  databaseMetricAge,
  engineName,
  expectedMembers,
  metricAvailable,
} from '../lib/database-view'
import { databaseActivityFresh, databaseHistorySamples, databaseActivityCounter } from '../lib/database-monitoring'
import { message, timestamp } from '../lib/api'
import { ServiceIcon } from './service-icon'
import { Copy, HeadingHelp, Status } from './shared'
import { ResourceMetric } from './resource-metric'

type Member = ManagedDatabase['observation']['members'][number]
export function DatabaseIdentity({ database: d }: { database: ManagedDatabase }) {
  return (
    <div className="db-identity">
      <ServiceIcon name={d.spec.engine} size={32} />
      <div>
        <strong>
          {engineName(d.spec.engine)} {d.spec.version}
        </strong>
        <span>
          {databaseLayoutSummary(d.spec)}{['clickhouse', 'oracle'].includes(d.spec.engine) && ' · Development preview'}
        </span>
      </div>
    </div>
  )
}
export function DatabaseSummary({ database: d, now }: { database: ManagedDatabase; now: number }) {
  const members = d.observation.members || []
  const nodes = [...new Set(members.map((m) => m.node).filter(Boolean))]
  const zones = [...new Set(members.map((m) => m.zone).filter(Boolean))]
  const health = databaseHealth(d, now)
  return (
    <dl className="db-summary">
      <div>
        <dt>Cluster health</dt>
        <dd>
          <Status value={health === 'Observation stale' ? 'stale' : health} />
        </dd>
        <small>
          Lifecycle: {d.status} · revision {d.revision}
        </small>
      </div>
      <div>
        <dt>{health === 'Observation stale' ? 'Members ready · last known' : 'Members ready'}</dt>
        <dd>
          {members.filter((m) => m.ready).length}
          <span> / {expectedMembers(d.spec)}</span>
        </dd>
        <small>
          {members.length} observed · {expectedMembers(d.spec)} desired
        </small>
      </div>
      <div>
        <dt>{health === 'Observation stale' ? 'Placement · last known' : 'Placement'}</dt>
        <dd>
          {nodes.length || '—'}
          <span> {nodes.length === 1 ? 'node' : 'nodes'}</span>
        </dd>
        <small>
          {zones.length > 0
            ? `${zones.length} observed ${zones.length === 1 ? 'zone' : 'zones'}`
            : nodes.length === 1 && members.length > 1
              ? 'All members share one node'
              : nodes.length
                ? 'Observed member placement'
                : 'Placement not observed'}
        </small>
      </div>
      <div>
        <dt>Configured storage</dt>
        <dd>
          {databaseStorageGiB(d.spec)}
          <span> GiB</span>
        </dd>
        <small>{d.spec.storage_gib} GiB per data member{d.spec.engine === 'mongodb' ? ' + 1 GiB logs' : ['clickhouse', 'oracle'].includes(d.spec.engine) ? ' + backup staging' : ''}{d.spec.engine === 'clickhouse' && d.spec.mode === 'cluster' ? ' + 3 GiB Keeper storage' : ''} · usage unavailable</small>
      </div>
    </dl>
  )
}

export function DatabaseMonitoring({
  database: d,
  now,
  receivedAt,
}: {
  database: ManagedDatabase
  now: number
  receivedAt: number
}) {
  const metrics = d.observation.metrics
  const [range, setRange] = useState<'1h' | '6h' | '24h'>('1h')
  const history = useQuery({
    queryKey: ['database-metrics', d.id, range],
    queryFn: ({ signal }) => unwrap(client.GET('/databases/{id}/metrics', { signal, params: { path: { id: d.id }, query: { range } } })),
    refetchInterval: 60_000,
    staleTime: 30_000,
    gcTime: 0,
  })
  const samples = databaseHistorySamples(history.data?.items)
  const engine = d.observation.engine_metrics
  const counter = databaseActivityCounter(d.spec.engine, engine)
  const engineFresh = databaseActivityFresh(d, receivedAt, now)
  const numeric = (value?: number) => engineFresh && value !== undefined && Number.isFinite(value) && value >= 0 ? value.toLocaleString() : 'Unavailable'

  const complete = metrics?.pods_sampled === expectedMembers(d.spec) && metrics?.pods_expected === expectedMembers(d.spec)
  const available =
    complete &&
    d.observation.revision === d.revision &&
    metricAvailable(metrics, d.observation.observed_at, receivedAt, now)
  const age = databaseMetricAge(metrics?.sampled_at, d.observation.observed_at, receivedAt, now)
  const capacity = databaseMemberCapacity(d.spec)
  return (
    <section className="db-panel db-monitoring" aria-label="Database monitoring">
      <div className="db-panel-heading">
        <div>
          <h2>Resource monitoring</h2>
          <HeadingHelp title="database monitoring">
            CPU and memory come from observed data-member resource samples. MySQL includes the database server and its sidecar; separate Router pods are excluded. MongoDB includes the database server and its agent. ClickHouse Keeper pods are excluded. Vitess member samples include MySQL and vttablet; gateways, topology and control services are excluded. The control plane retains one sample per minute for 24 hours, including when this page is closed. Missing observations and revision changes break the chart line. Configured capacity is not measured usage.
          </HeadingHelp>
        </div>
        <div className="db-monitor-controls">
          <Status value={available ? 'live' : complete && metrics?.available ? 'stale' : 'unavailable'} />
          <SelectField compact label="Monitoring history range" value={range} onValueChange={(value) => setRange(value as typeof range)} options={[{ value: '1h', label: 'Last hour' }, { value: '6h', label: 'Last 6 hours' }, { value: '24h', label: 'Last 24 hours' }]} />
        </div>
      </div>
      <div className="db-monitor-values node-runtime">
        <ResourceMetric
          capacityLabel="configured database limits"
          label="CPU usage"
          field="cpu"
          used={available ? metrics?.cpu_millicores : undefined}
          total={capacity ? capacity.cpu * 1000 : undefined}
          samples={samples}
        />
        <ResourceMetric
          capacityLabel="configured database limits"
          label="Memory usage"
          field="memory"
          used={available ? metrics?.memory_bytes : undefined}
          total={capacity ? capacity.memoryMiB * 1024 ** 2 : undefined}
          samples={samples}
        />
      </div>
      <dl className="db-facts db-monitor-facts">
        <div>
          <dt>Member samples</dt>
          <dd>
            {metrics?.pods_sampled ?? '—'} / {expectedMembers(d.spec)}
          </dd>
        </div>
        <div>
          <dt>Sample age</dt>
          <dd>{age === null ? 'Unavailable' : `${Math.floor(age / 1000)}s`}</dd>
        </div>
        <div>
          <dt>Per-member limit</dt>
          <dd>
            {d.spec.cpu} CPU · {d.spec.memory} RAM
            {d.spec.engine === 'vitess' && <span className="block text-muted-foreground">+ 100m CPU · 256Mi tablet</span>}
            {d.spec.engine === 'mysql' && <span className="block text-muted-foreground">+ 100m CPU · 256Mi sidecar</span>}
            {d.spec.engine === 'mongodb' && <span className="block text-muted-foreground">+ 100m CPU · 256Mi agent</span>}
          </dd>
        </div>
        <div>
          <dt>Source sample</dt>
          <dd>{metrics?.sampled_at ? timestamp(metrics.sampled_at) : 'Not received'}</dd>
        </div>
      </dl>
      {!available && (
        <p className="db-inline-notice">
          {complete && metrics?.available
            ? 'The last resource sample is stale or belongs to an earlier revision.'
            : metrics?.reason || 'Resource samples have not been received.'}
        </p>
      )}
      {history.isError && <p role="alert" className="db-inline-notice">History could not be loaded: {message(history.error)}</p>}
      <p className="db-monitor-footnote">
        {history.isPending ? 'Loading recorded history…' : `${samples.length} recorded resource samples · 1-minute resolution · 24-hour retention`}
      </p>
      <div className="db-panel-heading"><div><h2>Database activity</h2><HeadingHelp title="database activity">{d.spec.engine === 'vitess' ? 'Vitess engine activity metrics are unavailable until native telemetry is verified. Tablet, gateway and topology observations are shown separately.' : d.spec.engine === 'duckdb' ? 'MyDuck reports the persistent blocks allocated by DuckDB. This excludes temporary spill files and does not measure the whole volume. Connection counts, query counters and cache statistics are unavailable.' : d.spec.engine === 'oracle' ? 'Oracle connections count APP sessions. Transactions count commits and rollbacks across the instance. Data size is allocated APP segments. These metrics do not use licensed diagnostic packs.' : d.spec.engine === 'clickhouse' ? 'ClickHouse connections and server queries sum every data member, including internal queries and monitoring. Active data-part bytes count one replica per shard. Keeper is excluded; this is not total disk consumption.' : d.spec.engine === 'mongodb' ? 'MongoDB connections and operation counts cover the primary, including monitoring, agents and replication. Database size measures logical application data, excluding indexes and allocated files. Active connections are connections currently performing operations. Cache hit ratio and replication lag are unavailable.' : d.spec.engine === 'mysql' ? 'MySQL connections cover the application account on the primary. Database size estimates application table and index bytes. Server queries counts all queries on that primary, including administrative work. Cache hit ratio is unavailable.' : 'PostgreSQL statistics describe the application database on the primary. Redis statistics sum primary shards. Data size is PostgreSQL relation storage or Redis dataset memory.'} Connection counts represent database sessions, not application bindings. Data size is not filesystem usage. Counters reset when a server restarts. Query text and client identities are not collected.</HeadingHelp></div><Status value={engineFresh ? 'live' : 'unavailable'} /></div>
      <dl className="db-facts px-4 py-3">
        <div><dt>Connections</dt><dd>{numeric(engine?.connections)}</dd></div>
        <div><dt>Connection limit</dt><dd>{numeric(engine?.max_connections)}</dd></div>
        {d.spec.engine !== 'redis' && <div><dt>Active connections</dt><dd>{numeric(engine?.active_connections)}</dd></div>}
        <div><dt>{d.spec.engine === 'redis' ? 'Dataset memory' : 'Database size'}</dt><dd>{engineFresh && engine?.data_bytes !== undefined ? `${(engine.data_bytes / 1024 ** 2).toLocaleString(undefined, { maximumFractionDigits: 1 })} MiB` : 'Unavailable'}</dd></div>
        <div><dt>{counter.label}</dt><dd>{numeric(counter.value)}</dd></div>
        <div><dt>Cache hit ratio</dt><dd>{engineFresh && engine?.cache_hit_ratio !== undefined ? `${(engine.cache_hit_ratio * 100).toFixed(1)}%` : 'Unavailable'}</dd></div>
        {d.spec.engine === 'postgresql' && d.spec.replicas > 0 && <div><dt>Maximum replay backlog</dt><dd>{engineFresh && engine?.replication_lag_bytes !== undefined ? `${engine.replication_lag_bytes.toLocaleString()} bytes` : 'Unavailable'}</dd></div>}
        {d.spec.engine === 'redis' && <><div><dt>Evicted keys</dt><dd>{numeric(engine?.evicted_keys)}</dd></div><div><dt>Rejected connections</dt><dd>{numeric(engine?.rejected_connections)}</dd></div></>}
      </dl>
      {!engineFresh && <p className="db-inline-notice">{engine?.reason || 'Current database statistics have not been received.'}</p>}
      <p className="db-monitor-footnote">Query latency and filesystem usage are not collected.</p>
    </section>
  )
}

export function DatabaseMembers({
  database: d,
  now,
  receivedAt,
}: {
  database: ManagedDatabase
  now: number
  receivedAt: number
}) {
  const current = !['Observation stale', 'Not observed'].includes(databaseHealth(d, now))
  return (
    <section className="db-panel" aria-label="Database member details">
      <div className="db-panel-heading">
        <h2>Member inventory</h2>
        <span className="db-kicker">{d.observation.members?.length || 0} observed</span>
      </div>
      {!d.observation.members?.length ? (
        <p className="db-inline-notice">No members have been observed.</p>
      ) : (
        <div className="db-inventory">
          {d.observation.members.map((m: Member) => (
            <details key={m.uid} className="db-inventory-member">
              <summary>
                <span className="db-inventory-name">
                  <ServiceIcon name={d.spec.engine} size={22} />
                  <strong>{m.name}</strong>
                </span>
                <span className="db-inventory-role">{m.role}</span>
                <Status value={current ? (m.ready ? 'ready' : 'not ready') : 'stale'} />
                <span className="db-inventory-node">{m.node || 'Not scheduled'}</span>
                <span className="db-inventory-usage">
                  {current && metricAvailable(m.metrics, d.observation.observed_at, receivedAt, now)
                    ? `${m.metrics!.cpu_millicores!.toFixed(1)} mCPU · ${(m.metrics!.memory_bytes! / 1024 ** 2).toFixed(1)} MiB`
                    : 'Metrics unavailable'}
                </span>
                <span className="db-expand-label">Inspect</span>
              </summary>
              <dl className="db-facts db-member-expanded">
                <div>
                  <dt>Member ID</dt>
                  <dd>{m.uid}</dd>
                </div>
                <div>
                  <dt>Phase / restarts</dt>
                  <dd>
                    {m.phase || 'Not reported'} / {m.restarts ?? 'Not reported'}
                  </dd>
                </div>
                <div>
                  <dt>Created</dt>
                  <dd>{m.created_at ? timestamp(m.created_at) : 'Not reported'}</dd>
                </div>
                <div>
                  <dt>Zone / region</dt>
                  <dd>
                    {m.zone || 'Not reported'} / {m.region || 'Not reported'}
                  </dd>
                </div>
                <div>
                  <dt>Provider</dt>
                  <dd>{m.provider || 'Not reported'}</dd>
                </div>
                <div>
                  <dt>Storage allocation</dt>
                  <dd>{d.spec.storage_gib} GiB data{d.spec.engine === 'mongodb' ? ' + 1 GiB logs' : ['clickhouse', 'oracle'].includes(d.spec.engine) ? ` + ${d.spec.storage_gib} GiB backup staging` : ''}</dd>
                </div>
                <div className="sm:col-span-2">
                  <dt>Image</dt>
                  <dd className="db-image-id">
                    {m.image || 'Not reported'}
                    {m.image && <Copy value={m.image} />}
                  </dd>
                </div>
                {m.shard && (
                  <div className="sm:col-span-2">
                    <dt>Shard identity</dt>
                    <dd>{m.shard}</dd>
                  </div>
                )}
              </dl>
            </details>
          ))}
        </div>
      )}
    </section>
  )
}
