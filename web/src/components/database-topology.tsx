import { useEffect, useRef, useState, type KeyboardEvent } from 'react'
import { Link } from '@tanstack/react-router'
import { databaseHealth, useDatabaseConnections, type ManagedDatabase } from '../lib/databases'
import { engineName, endpointName, endpointAddress, metricAvailable, topologyGroups, shardedDatabase } from '../lib/database-view'
import {
  bindingEvidence,
  connectedApplications,
  databaseTopologyLayout,
  endpointTargetsMember,
  topologyApplicationPageSize,
} from '../lib/database-topology'
import { DatabaseStack } from './database-stack'
import { Button } from './ui/button'
import { Input } from './ui/input'
import { SelectField } from './ui/select'
import { Copy, HeadingHelp, Status } from './shared'

function activate(event: KeyboardEvent, callback: () => void) {
  if (event.key === 'Enter' || event.key === ' ') {
    event.preventDefault()
    callback()
  }
}
const shortName = (value: string, length = 22) =>
  value.length > length ? `${value.slice(0, length - 1)}…` : value

function revealInCanvas(viewport: HTMLDivElement, id: string) {
  const element = viewport.querySelector(`[data-node="${CSS.escape(id)}"]`)
  if (!element) return
  const node = element.getBoundingClientRect(), visible = viewport.getBoundingClientRect()
  if (node.left < visible.left + 12) viewport.scrollLeft += node.left - visible.left - 12
  else if (node.right > visible.right - 12) viewport.scrollLeft += node.right - visible.right + 12
  if (node.top < visible.top + 12) viewport.scrollTop += node.top - visible.top - 12
  else if (node.bottom > visible.bottom - 12) viewport.scrollTop += node.bottom - visible.bottom + 12
  return element
}

export function DatabaseTopology({
  database: d,
  now,
  receivedAt,
}: {
  database: ManagedDatabase
  now: number
  receivedAt: number
}) {
  const connections = useDatabaseConnections(d.id)
  const groups = topologyGroups(d)
  const [groupId, setGroupId] = useState('')
  const group = groups.find((g) => g.id === groupId) || groups[0]
  const members = [...(group?.members || [])].sort(
    (a, b) =>
      Number(b.role === 'primary') - Number(a.role === 'primary') || a.name.localeCompare(b.name),
  )
  const primary = members.find((m) => m.role === 'primary')
  const [selected, setSelected] = useState('')
  const [filter, setFilter] = useState('')
  const [page, setPage] = useState(0)
  const canvas = useRef<HTMLDivElement>(null)
  const applications = connectedApplications(connections.data?.items || [])
  const filtered = applications.filter((app) =>
    `${app.displayName} ${app.name} ${app.bindings.map((b) => `${b.service} ${b.variable}`).join(' ')}`
      .toLowerCase()
      .includes(filter.toLowerCase()),
  )
  const pageCount = Math.max(1, Math.ceil(filtered.length / topologyApplicationPageSize))
  const activePage = Math.min(page, pageCount - 1)
  const shown = filtered.slice(
    activePage * topologyApplicationPageSize,
    (activePage + 1) * topologyApplicationPageSize,
  )
  const application =
    shown.find((app) => `app:${app.id}` === selected) || (!selected ? shown[0] : undefined)
  const endpoints = d.observation.endpoints || []
  const endpoint = endpoints.find((e) => `endpoint:${e.purpose}` === selected)
  const vitess = d.spec.engine === 'vitess'
  const routed = d.spec.engine === 'mysql' || vitess
  const poolers = (routed ? d.observation.routing?.members : d.observation.pooling?.members) || []
  const middleLabel = vitess ? 'Gateway' : routed ? 'Router' : 'Pooler'
  const coordinatorLabel = vitess ? 'Topology' : 'Keeper'
  const pooler = poolers.find((m) => `pooler:${m.uid}` === selected)
  const coordinators = d.spec.engine === 'clickhouse' || vitess ? d.observation.coordination?.members || [] : []
  const coordinator = coordinators.find((m) => `keeper:${m.uid}` === selected)
  const active =
    application || endpoint || pooler || coordinator
      ? undefined
      : members.find((m) => `member:${m.uid}` === selected) || members[0]
  const activeID = application
    ? `app:${application.id}`
    : endpoint
      ? `endpoint:${endpoint.purpose}`
      : pooler
        ? `pooler:${pooler.uid}`
      : coordinator
        ? `keeper:${coordinator.uid}`
      : active
        ? `member:${active.uid}`
        : ''
  const layout = databaseTopologyLayout(shown.length, members.length, Boolean(primary), endpoints.length, poolers.length, coordinators.length)
  useEffect(() => {
    canvas.current?.scrollTo({ left: 0, top: 0 })
  }, [filter, activePage, groupId])
  const endpointPositions = layout.endpointPositions
  const inspectedMember = pooler || coordinator || active
  const current = !['Observation stale', 'Not observed'].includes(databaseHealth(d, now))
  const connectionsStale = connections.isError || now - connections.dataUpdatedAt > 30000
  useEffect(() => {
    const viewport = canvas.current
    if (!viewport || !activeID) return
    let width = viewport.clientWidth, height = viewport.clientHeight
    const observer = new ResizeObserver(() => {
      if (width === viewport.clientWidth && height === viewport.clientHeight) return
      width = viewport.clientWidth
      height = viewport.clientHeight
      revealInCanvas(viewport, activeID)
    })
    observer.observe(viewport)
    return () => observer.disconnect()
  }, [activeID])
  const reveal = (id: string) => {
    setSelected(id)
    const viewport = canvas.current
    const element = viewport && revealInCanvas(viewport, id)
    if (!element) return
    requestAnimationFrame(() => {
      const bounds = element.getBoundingClientRect()
      if (bounds.top < 0 || bounds.bottom > window.innerHeight)
        element.scrollIntoView({ block: 'nearest', inline: 'nearest' })
    })
  }
  return (
    <section className="db-panel db-topology" aria-label="Database topology">
      <div className="db-panel-heading">
        <div>
          <h2>Topology</h2>
          <HeadingHelp title="database topology">
            Applications come from saved bindings and deployment records. Lines show configured
            endpoint routes and controller-reported replication relationships. They do not measure
            live sessions, traffic or replication lag.
          </HeadingHelp>
        </div>
        <span className="db-kicker">
          {connections.data
            ? `${applications.length} application${applications.length === 1 ? '' : 's'}`
            : connections.isPending
              ? 'Loading applications'
              : 'Applications unavailable'}{' '}
          · {members.length}{' '}
          {shardedDatabase(d.spec.engine) && d.spec.mode === 'cluster'
            ? `member${members.length === 1 ? '' : 's'} in this shard`
            : `member${members.length === 1 ? '' : 's'}`}
        </span>
      </div>
      <div className="db-topology-toolbar">
        <label className="db-topology-search">
          <span>Find application or service</span>
          <Input
            value={filter}
            onChange={(event) => {
              setFilter(event.target.value)
              setPage(0)
              setSelected('')
            }}
            placeholder="Search connections"
          />
        </label>
        {shardedDatabase(d.spec.engine) && d.spec.mode === 'cluster' && groups.length > 0 && (
          <SelectField
            label="Topology shard"
            value={group?.id || ''}
            onValueChange={(value) => {
              setGroupId(value)
              setSelected('')
            }}
            options={groups.map((g) => ({ value: g.id, label: g.label }))}
          />
        )}
        <div className="db-topology-paging">
          <span aria-live="polite">
            {!connections.data
              ? '—'
              : filtered.length
                ? `${activePage * topologyApplicationPageSize + 1}–${Math.min((activePage + 1) * topologyApplicationPageSize, filtered.length)} of ${filtered.length}`
                : '0'}{' '}
            applications
          </span>
          {pageCount > 1 && (
            <>
              <Button
                size="sm"
                disabled={activePage === 0}
                onClick={() => {
                  setPage(activePage - 1)
                  setSelected('')
                }}
              >
                Previous
              </Button>
              <Button
                size="sm"
                disabled={activePage === pageCount - 1}
                onClick={() => {
                  setPage(activePage + 1)
                  setSelected('')
                }}
              >
                Next
              </Button>
            </>
          )}
        </div>
      </div>
      {d.spec.engine === 'redis' && d.spec.mode === 'cluster' && (
        <div className="db-routing-strip">
          <span>{d.observation.slots_assigned ?? '—'} / 16,384 slots assigned</span>
          <span>
            {current && d.observation.slots_healthy
              ? 'Slot coverage healthy'
              : current
                ? 'Slot health not verified'
                : 'Slot observation stale'}
          </span>
        </div>
      )}
      {vitess && <div className="db-routing-strip"><span>{poolers.length} vtgate gateways observed</span><span>{coordinators.length} / 3 topology members observed</span><span>{d.spec.replicas > 0 ? 'app@primary routes writes; app@replica routes replica reads.' : 'app@primary routes reads and writes.'}</span></div>}
      {d.spec.engine === 'clickhouse' && d.spec.mode === 'cluster' && <div className="db-routing-strip">
        <span>{coordinators.length} / 3 Keeper members observed</span>
        <span>{!current ? 'Keeper observation stale' : d.observation.coordination?.ready ? 'Keeper quorum verified' : d.observation.coordination?.message || 'Keeper quorum not verified'}</span>
        <span>Distributed tables combine shards; local tables contain one shard.</span>
      </div>}
      {connections.isPending && (
        <p className="db-inline-notice" role="status">
          Loading application connections…
        </p>
      )}
      {connections.isError && (
        <div className="db-inline-notice" role="alert">
          Application connections could not be refreshed.
          {connections.data ? ' Showing the last received records.' : ''}{' '}
          <Button size="sm" onClick={() => void connections.refetch()}>
            Retry connections
          </Button>
        </div>
      )}
      {connections.data && connectionsStale && !connections.isError && (
        <p className="db-inline-notice">
          Application binding records have not refreshed recently. Showing the last received
          records.
        </p>
      )}
      {connections.data?.items.some((b) => !endpoints.some((e) => e.purpose === b.endpoint)) && (
        <p className="db-inline-notice">
          Some bindings refer to endpoints that are not currently observed. Select an application to
          inspect those records.
        </p>
      )}
      {connections.data?.truncated && (
        <p className="db-inline-notice">
          Showing the first {connections.data.limit} binding records. Some applications or bindings
          may not be included.
        </p>
      )}
      <div className="db-member-picker db-topology-picker">
        <SelectField
          label="Inspect topology"
          value={activeID}
          onValueChange={reveal}
          options={[
            ...shown.map((app) => ({
              value: `app:${app.id}`,
              label: `Application · ${app.displayName}`,
            })),
            ...endpoints.map((e) => ({
              value: `endpoint:${e.purpose}`,
              label: `Endpoint · ${endpointName(e.purpose, d.spec.engine)}`,
            })),
            ...poolers.map((m) => ({ value: `pooler:${m.uid}`, label: `${middleLabel} · ${m.name}` })),
            ...members.map((m) => ({ value: `member:${m.uid}`, label: `${m.role} · ${m.name}` })),
            ...coordinators.map((m) => ({ value: `keeper:${m.uid}`, label: `${coordinatorLabel} ${m.role} · ${m.name}` })),
          ]}
        />
        <Button size="sm" disabled={!activeID} onClick={() => reveal(activeID)}>
          Locate selected
        </Button>
      </div>
      <div
        className="db-topology-scroll"
        ref={canvas}
        tabIndex={0}
        role="region"
        aria-label="Database topology canvas. Scroll to explore applications, endpoints and members."
      >
        <svg
          width={layout.width}
          height={layout.height}
          style={{ width: layout.width, height: layout.height }}
          viewBox={`0 0 ${layout.width} ${layout.height}`}
          className="db-topology-art"
          role="group"
          aria-label={`${engineName(d.spec.engine)} application and member topology`}
        >
          <text x="28" y="36" className="db-lane-title">
            APPLICATIONS
          </text>
          <text x={layout.endpointX} y="36" className="db-lane-title">
            PRIVATE ENDPOINTS
          </text>
          {poolers.length > 0 && <text x={layout.poolerX} y="36" className="db-lane-title">{vitess ? 'VTGATE' : routed ? 'MYSQL ROUTER' : 'PGBOUNCER'}</text>}
          <text x={layout.memberStart + 20} y="36" className="db-lane-title">
            {current ? 'OBSERVED MEMBERS' : 'LAST KNOWN MEMBERS'}
          </text>
          <path
            className="db-lane-divider"
            d={`M${layout.applicationWidth + 20},58 V${layout.height - 24} M${layout.memberStart - 25},58 V${layout.height - 24}`}
          />
          {!shown.length && (
            <text x="28" y="120" className="db-stack-role">
              {connections.isPending
                ? 'Loading connections'
                : connections.isError && !connections.data
                  ? 'Connections unavailable'
                  : filter
                    ? 'No matching applications'
                    : 'No recorded application bindings'}
            </text>
          )}
          {!endpoints.length && (
            <text x={layout.endpointX} y="120" className="db-stack-role">
              Not observed
            </text>
          )}
          {!members.length && (
            <text x={layout.memberStart + 20} y="120" className="db-stack-role">
              Waiting for member observation
            </text>
          )}
          {shown.flatMap((app, i) =>
            [...new Set(app.bindings.map((b) => b.endpoint))].flatMap((purpose) => {
              const target = endpoints.findIndex((e) => e.purpose === purpose)
              if (target < 0) return []
              const a = layout.appPositions[i],
                b = endpointPositions[target],
                gutter = a.y + 85 + target * 6,
                trunk = layout.applicationWidth + 4 + target * 5
              return (
                <path
                  key={`${app.id}-${purpose}`}
                  className={`db-binding-edge ${application?.id === app.id ? 'db-binding-edge-selected' : ''} ${connectionsStale ? 'db-edge-stale' : ''}`}
                  d={`M${a.x + 82},${a.y + 70} V${gutter} H${trunk} V${b.y + 35} H${b.x}`}
                />
              )
            }),
          )}
          {endpoints.flatMap((e, i) =>
            members.flatMap((m, j) => {
              if (routed || e.purpose.startsWith('pooled_') || !endpointTargetsMember(e.purpose, m.role, d.spec.engine))
                return []
              const from = endpointPositions[i],
                to = layout.memberPositions[j],
                trunk = layout.memberStart - 12 + i * 7
              return (
                <path
                  key={`${e.purpose}-${m.uid}`}
                  className={`db-route-edge ${!current ? 'db-edge-stale' : ''}`}
                  d={poolers.length ? `M${from.x + 155},${from.y + 35} H${layout.poolerX - 30 + i * 5} V${62 + i * 10} H${trunk} V${to.y - 72} H${to.x} V${to.y - 55}` : `M${from.x + 155},${from.y + 35} H${trunk} V${to.y - 72} H${to.x} V${to.y - 55}`}
                />
              )
            }),
          )}
          {endpoints.flatMap((e, i) => poolers.flatMap((p, j) => {
            if (!routed && e.purpose !== p.role) return []
            const from = endpointPositions[i], to = layout.poolerPositions[j]
            return <path key={`${e.purpose}-${p.uid}`} className={`db-route-edge ${!current ? 'db-edge-stale' : ''}`} d={`M${from.x + 155},${from.y + 35} H${layout.poolerX - 20 + i * 4} V${to.y + 35} H${to.x}`} />
          }))}
          {poolers.flatMap((p, i) => members.flatMap((m, j) => {
            if (routed ? !['primary', 'replica'].includes(m.role) : !endpointTargetsMember(p.role, m.role, d.spec.engine)) return []
            const from = layout.poolerPositions[i], to = layout.memberPositions[j]
            return <path key={`${p.uid}-${m.uid}`} className={`db-route-edge ${!current ? 'db-edge-stale' : ''}`} d={`M${from.x + 164},${from.y + 35} H${layout.memberStart - 20 + i * 3} V${to.y - 72} H${to.x} V${to.y - 55}`} />
          }))}
          {primary &&
            members.map(
              (m, i) =>
                m.role === 'replica' && (
                  <path
                    key={`replication-${m.uid}`}
                    className={`db-replication-edge ${!current ? 'db-edge-stale' : ''}`}
                    d={`M${layout.memberPositions[0].x},${layout.memberPositions[0].y + 110} H${layout.memberStart + 12} V${layout.memberPositions[i].y - 82} H${layout.memberPositions[i].x} V${layout.memberPositions[i].y - 55}`}
                  />
                ),
            )}
          {coordinators.length > 0 && <>
            <text x={layout.memberStart + 20} y={layout.coordinationY - 20} className="db-lane-title">{vitess ? current ? 'ETCD TOPOLOGY' : 'LAST KNOWN TOPOLOGY' : current ? 'KEEPER COORDINATION' : 'LAST KNOWN KEEPERS'}</text>
            {members.map((m, i) => <path key={`coordination-data-${m.uid}`} className={`db-replication-edge ${!current ? 'db-edge-stale' : ''}`} d={`M${layout.memberPositions[i].x},${layout.memberPositions[i].y + 110} H${layout.memberStart - 8} V${layout.coordinationY - 45} H${layout.memberStart + 90}`} />)}
            {coordinators.map((m, i) => <path key={`coordination-keeper-${m.uid}`} className={`db-replication-edge ${!current ? 'db-edge-stale' : ''}`} d={`M${layout.memberStart + 90},${layout.coordinationY - 45} H${layout.coordinatorPositions[i].x + 82} V${layout.coordinationY}`} />)}
          </>}
          {shown.map((app, i) => {
            const at = layout.appPositions[i],
              id = `app:${app.id}`
            return (
              <g
                key={id}
                data-node={id}
                role="button"
                tabIndex={0}
                aria-pressed={activeID === id}
                aria-label={`Inspect application ${app.displayName}, ${app.bindings.length} bindings`}
                className={`db-application-node ${activeID === id ? 'db-node-selected' : ''}`}
                transform={`translate(${at.x} ${at.y})`}
                onClick={() => setSelected(id)}
                onFocus={() => reveal(id)}
                onKeyDown={(event) => activate(event, () => setSelected(id))}
              >
                <title>{app.displayName}</title>
                <rect className="db-application-box" width="164" height="70" rx="5" />
                <path
                  className="db-app-cube"
                  d="M13,20 L23,15 L33,20 L23,25 Z M13,20 V31 L23,37 L33,31 V20 M23,25 V37"
                />
                <text x="42" y="29" className="db-app-title">
                  {shortName(app.displayName, 16)}
                </text>
                <text x="13" y="54" className="db-app-meta">
                  {app.bindings.length} {app.bindings.length === 1 ? 'binding' : 'bindings'} ·{' '}
                  {app.bindings.some((b) => b.saved_revision > 0) ? 'saved' : 'history'}
                </text>
              </g>
            )
          })}
          {endpoints.map((e, i) => {
            const at = endpointPositions[i],
              id = `endpoint:${e.purpose}`
            return (
              <g
                key={id}
                data-node={id}
                role="button"
                tabIndex={0}
                aria-pressed={activeID === id}
                aria-label={`Inspect endpoint: ${endpointName(e.purpose, d.spec.engine)}`}
                className={`db-endpoint-node ${activeID === id ? 'db-node-selected' : ''}`}
                transform={`translate(${at.x} ${at.y})`}
                onClick={() => setSelected(id)}
                onFocus={() => reveal(id)}
                onKeyDown={(event) => activate(event, () => setSelected(id))}
              >
                <rect className="db-application-box" width="155" height="70" rx="5" />
                <text x="12" y="29" className="db-app-title">
                  {endpointName(e.purpose, d.spec.engine)}
                </text>
                <text x="12" y="53" className="db-stack-role">
                  {e.port} · {current ? 'private' : 'last known'}
                </text>
              </g>
            )
          })}
          {poolers.map((p, i) => {
            const at = layout.poolerPositions[i], id = `pooler:${p.uid}`
            return <g key={id} data-node={id} role="button" tabIndex={0} aria-pressed={activeID === id} aria-label={`Inspect ${middleLabel.toLowerCase()} ${p.name}, ${current ? p.ready ? 'ready' : 'not ready' : 'last known state'}`} className={`db-application-node ${activeID === id ? 'db-node-selected' : ''}`} transform={`translate(${at.x} ${at.y})`} onClick={() => setSelected(id)} onFocus={() => reveal(id)} onKeyDown={(event) => activate(event, () => setSelected(id))}>
              <title>{p.name}</title><rect className="db-application-box" width="164" height="70" rx="5" />
              <text x="12" y="28" className="db-app-title">{vitess ? 'vtgate' : routed ? 'MySQL Router' : p.role === 'pooled_read_only' ? 'Read pooler' : 'Write pooler'} {poolers.slice(0, i + 1).filter((member) => member.role === p.role).length}</text>
              <text x="12" y="52" className="db-app-meta">{current ? p.ready ? 'Ready' : 'Not ready' : 'Last known'}{!routed && ` · ${d.spec.pooling?.mode}`}</text>
            </g>
          })}
          {coordinators.map((m, i) => {
            const at = layout.coordinatorPositions[i], id = `keeper:${m.uid}`
            return <g key={id} data-node={id} role="button" tabIndex={0} aria-pressed={activeID === id} aria-label={`Inspect ${coordinatorLabel} ${m.name}, ${m.role}, ${current ? m.ready ? 'ready' : 'not ready' : 'last known state'}`} className={`db-application-node ${activeID === id ? 'db-node-selected' : ''}`} transform={`translate(${at.x} ${at.y})`} onClick={() => setSelected(id)} onFocus={() => reveal(id)} onKeyDown={(event) => activate(event, () => setSelected(id))}>
              <title>{m.name}</title><rect className="db-application-box" width="164" height="70" rx="5" />
              <text x="12" y="28" className="db-app-title">{coordinatorLabel} {i + 1} · {m.role}</text>
              <text x="12" y="52" className="db-app-meta">{current ? m.ready ? 'Ready' : 'Not ready' : 'Last known'}</text>
            </g>
          })}
          {members.map((m, i) => {
            const at = layout.memberPositions[i],
              id = `member:${m.uid}`
            return (
              <g
                key={id}
                data-node={id}
                role="button"
                tabIndex={0}
                aria-pressed={activeID === id}
                aria-label={`Inspect ${m.name}, ${m.role}, ${current ? (m.ready ? 'ready' : 'not ready') : 'last known state'}`}
                className="db-topology-member"
                onClick={() => setSelected(id)}
                onFocus={() => reveal(id)}
                onKeyDown={(event) => activate(event, () => setSelected(id))}
              >
                <title>{m.name}</title>
                <rect
                  x={at.x - 82}
                  y={at.y - 67}
                  width="164"
                  height="174"
                  rx="8"
                  className="db-stack-hit"
                />
                <DatabaseStack
                  {...at}
                  primary={m.role === 'primary'}
                  ready={current && m.ready}
                  selected={activeID === id}
                  unknown={!['primary', 'replica'].includes(m.role)}
                />
                <text x={at.x} y={at.y + 78} textAnchor="middle" className="db-stack-name">
                  {shortName(m.name)}
                </text>
                <text x={at.x} y={at.y + 98} textAnchor="middle" className="db-stack-role">
                  {m.role.toUpperCase()}
                </text>
              </g>
            )
          })}
        </svg>
      </div>
      <div className="db-topology-legend">
        {!(d.spec.engine === 'clickhouse' && d.spec.mode === 'cluster') && <span>
          <i className="db-legend-primary" />
          Primary
        </span>}
        <span>
          <i />
          Replica
        </span>
        {members.some((m) => !['primary', 'replica'].includes(m.role)) && (
          <span>
            <i className="db-legend-unknown" />
            Unknown role
          </span>
        )}
        <span>
          <b className="db-line-key" />
          Binding
        </span>
        <span>
          <b className="db-line-key db-line-key-route" />
          Endpoint route
        </span>
        <span>
          <b className="db-line-key db-line-key-replication" />
          {coordinators.length ? 'Coordination' : 'Replication'}
        </span>
        <span className="ml-auto">Scroll canvas to explore · select to inspect</span>
      </div>

      {application && (
        <div className="db-member-inspector" aria-label="Application connection inspector">
          <div className="flex flex-wrap items-center justify-between gap-3">
            <strong className="break-all">{application.displayName}</strong>
            <Button asChild size="sm">
              <Link to="/applications/$applicationId" params={{ applicationId: application.id }}>
                Open application
              </Link>
            </Button>
          </div>
          <div className="db-binding-list">
            {application.bindings.map((b) => (
              <div key={`${b.service}-${b.variable}-${b.endpoint}`} className="db-binding-detail">
                <strong>
                  {b.service} · {b.variable}
                </strong>
                <span>
                  {endpointName(b.endpoint, d.spec.engine)}
                  {!endpoints.some((e) => e.purpose === b.endpoint)
                    ? ' · endpoint not currently observed'
                    : !current
                      ? ' · endpoint observation stale'
                      : ''}
                </span>
                <small>{bindingEvidence(b)}</small>
              </div>
            ))}
          </div>
          <p className="text-xs text-muted-foreground">
            Binding records do not confirm a live connection. Applications using manually entered
            credentials are not discoverable here.
          </p>
        </div>
      )}
      {endpoint && (
        <div className="db-member-inspector" aria-label="Endpoint inspector">
          <strong>{endpointName(endpoint.purpose, d.spec.engine)}</strong>
          <div className="flex min-w-0 items-center gap-2">
            <code className="break-all text-xs">
              {endpointAddress(endpoint, d.spec.engine)}
            </code>
            <Copy value={endpointAddress(endpoint, d.spec.engine)} />
          </div>
          <p className="text-xs text-muted-foreground">
            {vitess ? `vtgate routes the MySQL target ${endpoint.purpose === 'read_only' ? 'app@replica to replicas; reads may lag' : 'app@primary to the primary of each shard'}. Table routing follows the reviewed VSchema. Clients must reconnect after failover.` : endpoint.purpose === 'pooled_read_write'
              ? 'PgBouncer reuses connections to the write service. Applications must retry after failover.'
              : endpoint.purpose === 'pooled_read_only'
                ? 'PgBouncer reuses connections to the replica service. Replica reads can lag, and existing sessions must reconnect after failover.'
              : endpoint.purpose === 'read_write'
              ? 'Routes to the primary.'
              : endpoint.purpose === 'read_only'
                ? 'Routes to replicas. Replica reads can lag behind the primary.'
                : d.spec.engine === 'clickhouse'
                  ? 'Connects to any data member. A Distributed table uses the managed cluster definition to route inserts and combine shards. A local table contains only its shard; the endpoint does not rewrite SQL.'
                : d.spec.engine === 'mongodb'
                  ? 'Discovers every replica-set member. The MongoDB driver finds the primary and applies your read preference. Every advertised member must be reachable.'
                  : 'Use a cluster-aware client to discover shard owners. This is not a query router.'}
          </p>
        </div>
      )}
      {inspectedMember && (
        <div className="db-member-inspector" aria-label={pooler ? `${middleLabel} inspector` : coordinator ? `${coordinatorLabel} member inspector` : "Database member inspector"}>
          <div className="flex flex-wrap items-center gap-3">
            <strong className="font-mono break-all">{inspectedMember.name}</strong>
            <Status value={current ? (inspectedMember.ready ? 'ready' : 'not ready') : 'stale'} />
          </div>
          {coordinator && <p className="text-xs text-muted-foreground">{vitess ? 'The three etcd topology members store routing metadata, not application tables. Each reserves 100m CPU, 256Mi memory and 1 GiB storage.' : 'Keeper coordinates replicated metadata and does not store application tables. Each member reserves 250m CPU, 256Mi memory and 1 GiB storage.'}</p>}
          <dl className="db-facts db-inspector-facts">
            {pooler && routed && <div><dt>Routes</dt><dd>{d.spec.mode === 'cluster' ? 'Primary writes · replica reads' : 'Primary reads and writes'}</dd></div>}
            <div>
              <dt>Role</dt>
              <dd>{pooler ? endpointName(inspectedMember.role, d.spec.engine) : inspectedMember.role}</dd>
            </div>
            <div>
              <dt>Node</dt>
              <dd>{inspectedMember.node || 'Not scheduled'}</dd>
            </div>
            <div>
              <dt>Zone / region</dt>
              <dd>
                {inspectedMember.zone || 'Not reported'} / {inspectedMember.region || 'Not reported'}
              </dd>
            </div>
            <div>
              <dt>Provider</dt>
              <dd>{inspectedMember.provider || 'Not reported'}</dd>
            </div>
            <div>
              <dt>CPU</dt>
              <dd>
                {current &&
                metricAvailable(inspectedMember.metrics, d.observation.observed_at, receivedAt, now)
                  ? `${inspectedMember.metrics!.cpu_millicores!.toFixed(1)} mCPU`
                  : 'Unavailable'}
              </dd>
            </div>
            <div>
              <dt>Memory</dt>
              <dd>
                {current &&
                metricAvailable(inspectedMember.metrics, d.observation.observed_at, receivedAt, now)
                  ? `${(inspectedMember.metrics!.memory_bytes! / 1024 ** 2).toFixed(1)} MiB`
                  : 'Unavailable'}
              </dd>
            </div>
          </dl>
        </div>
      )}
      <div className="db-monitor-footnote">
        {d.spec.placement?.spread === 'zones'
          ? 'Required: one member per zone.'
          : d.spec.placement?.spread === 'nodes'
            ? 'Required: one member per node.'
            : 'Placement uses scheduler defaults.'}{' '}
        {d.observation.placement?.message ||
          'Zone and provider details depend on reported node metadata.'}
      </div>
    </section>
  )
}
