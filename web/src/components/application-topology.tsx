import { useDatabases, databaseHealth } from '../lib/databases'
import { useScope, canAccess } from '../lib/scope'
import { Button } from './ui/button'
import { ManagedDatabaseConnections } from './managed-database-connections'
import { serviceProfileLabel } from '../lib/service-resources'
import { useEditionFeatures } from '../lib/dashboard-edition'
import { useState } from 'react'
import { Link } from '@tanstack/react-router'
import { Badge, Card } from './ui/surfaces'
import { Brackets } from '@hakopod/hatch-ui/components/brackets'
import type { Application } from '../lib/types'
import { runtimeReplicaSummary, serviceRuntimeHealth } from '../lib/runtime-health'
import { Icon } from './icons'
import { ServiceIcon } from './service-icon'
import { HeadingHelp, Copy, Status, Note } from './shared'
export default function ApplicationTopology({ application: app }: { application: Application }) {
  const features = useEditionFeatures()
  const names = Object.keys(app.spec.services).slice(0, 32)
  const databaseIDs = [
    ...new Set(
      names.flatMap((name) =>
        Object.values(app.spec.services[name].bindings || {})
          .map((b) => b.managed_database)
          .filter((id): id is string => Boolean(id)),
      ),
    ),
  ].slice(0, 32)
  const { identity } = useScope()
  const databases = useDatabases(
    app.project,
    app.environment,
    !identity.application &&
      canAccess(identity, app.project, 'deployments:read') &&
      databaseIDs.length > 0,
  )
  const databaseByID = new Map(databases.data?.items.map((d) => [d.id, d]))
  const nodes = [...names, ...databaseIDs.map((id) => `database:${id}`)]
  const [selected, setSelected] = useState(names[0] || '')
  const name = names.includes(selected) ? selected : names[0]
  const service = app.spec.services[name]
  const observed = app.observed?.services?.find((item) => item.name === name)
  const selectedHealth = serviceRuntimeHealth(observed, app.observed?.observed_at)
  const positions = new Map(
    nodes.map((name, index) => [
      name,
      { x: 32 + (index % 3) * 260, y: 52 + Math.floor(index / 3) * 156 },
    ]),
  )
  const height = Math.max(280, Math.ceil(nodes.length / 3) * 156 + 65)
  const dependencies = names.flatMap((name) =>
    (app.spec.services[name].depends_on || [])
      .filter((dependency) => positions.has(dependency))
      .map((dependency) => ({ from: name, to: dependency })),
  )
  const bindings = names.flatMap((name) =>
    [
      ...new Set(
        Object.values(app.spec.services[name].bindings || {}).map((b) => b.managed_database),
      ),
    ]
      .filter((id): id is string => Boolean(id && databaseIDs.includes(id)))
      .map((id) => ({ from: name, to: `database:${id}` })),
  )
  const edges = [...dependencies, ...bindings]
  function revealNode(element: HTMLElement) {
    const viewport = element.closest('.topology-viewport')
    if (!(viewport instanceof HTMLElement)) return
    const node = element.getBoundingClientRect(),
      visible = viewport.getBoundingClientRect()
    if (node.left < visible.left + 8) viewport.scrollLeft += node.left - visible.left - 8
    else if (node.right > visible.right - 8) viewport.scrollLeft += node.right - visible.right + 8
  }
  return (
    <div className="ops-topology">
      <div className="section-toolbar">
        <div>
          <div className="hako-section-heading-title">
            <h2>Service topology</h2>
            <HeadingHelp title="Service topology">
              Saved services, readiness dependencies and managed database connections. Select a
              service to inspect it. Database health comes from its own controller.
            </HeadingHelp>
          </div>
        </div>
        <Badge>
          {names.length} services · {databaseIDs.length} databases · {dependencies.length}{' '}
          dependencies
        </Badge>
      </div>
      <div className="topology-layout">
        <div className="topology-viewport">
          <div className="topology-canvas" style={{ height, width: 810 }}>
            <svg className="topology-edges" width="810" height={height} aria-hidden="true">
              <defs>
                <marker
                  id="dependency-arrow"
                  viewBox="0 0 10 10"
                  refX="9"
                  refY="5"
                  markerWidth="5"
                  markerHeight="5"
                  orient="auto-start-reverse"
                >
                  <path d="M 0 0 L 10 5 L 0 10 z" fill="currentColor" />
                </marker>
              </defs>
              {edges.map(({ from, to }) => {
                const a = positions.get(from)!
                const b = positions.get(to)!
                const sameRow = a.y === b.y
                return (
                  <path
                    key={`${from}-${to}`}
                    className={from === name || to === name ? 'edge-selected' : ''}
                    d={
                      sameRow
                        ? `M${a.x + 104} ${a.y + 101} C${a.x + 104} ${a.y + 146} ${b.x + 104} ${b.y + 146} ${b.x + 104} ${b.y + 101}`
                        : `M${a.x + 104} ${a.y + 101} C${a.x + 104} ${a.y + 135} ${b.x + 104} ${b.y - 30} ${b.x + 104} ${b.y}`
                    }
                    markerEnd="url(#dependency-arrow)"
                  />
                )
              })}
            </svg>
            {names.map((item) => {
              const at = positions.get(item)!
              const config = app.spec.services[item]
              const state = app.observed?.services?.find((entry) => entry.name === item)
              const health = serviceRuntimeHealth(state, app.observed?.observed_at)
              return (
                <button
                  type="button"
                  key={item}
                  className={`topology-node interactive ${name === item ? 'selected hatch' : ''}`}
                  style={{ left: at.x, top: at.y }}
                  onFocus={(event) => revealNode(event.currentTarget)}
                  aria-pressed={name === item}
                  onClick={() => setSelected(item)}
                >
                  <Brackets />
                  <Status value={health.status} small />
                  <div className="min-w-0">
                    {config.actions ? (
                      <ServiceIcon name="github" size={18} />
                    ) : (
                      <Icon
                        name={config.public ? 'globe' : config.port ? 'box' : 'terminal'}
                        size={18}
                      />
                    )}
                    <strong className="min-w-0 truncate">
                      {app.service_display_names?.[item] || item}
                    </strong>
                    <Icon name="chevron" size={13} />
                  </div>
                  <small>
                    {runtimeReplicaSummary(health)} ·{' '}
                    {config.public ? 'Public HTTP' : config.port ? 'Private' : 'Worker'}
                    {config.volume ? ' · Volume' : ''}
                  </small>
                </button>
              )
            })}
            {databaseIDs.map((id) => {
              const at = positions.get(`database:${id}`)!
              const d = databaseByID.get(id)
              return (
                <Button
                  asChild
                  key={id}
                  onFocus={(event) => revealNode(event.currentTarget)}
                  className="topology-node grid-cols-[minmax(0,1fr)] justify-items-start text-left normal-case tracking-normal [&>*]:min-w-0 [&>*]:max-w-full [&>*]:truncate"
                  style={{ left: at.x, top: at.y }}
                >
                  <Link
                    to="/databases/$databaseId"
                    params={{ databaseId: id }}
                    search={{ project: app.project, environment: app.environment }}
                  >
                    <span className="text-xs text-muted-foreground">
                      {d ? databaseHealth(d) : 'Managed database'}
                    </span>
                    <strong className="block truncate">{d?.spec.name || id.slice(0, 8)}</strong>
                    <small>
                      {d
                        ? `${d.spec.engine === 'redis' ? 'Redis' : 'PostgreSQL'} · ${d.spec.shards * (1 + d.spec.replicas)} configured members`
                        : 'Open database details'}
                    </small>
                  </Link>
                </Button>
              )
            })}
            <div className="topology-legend">
              <span className="dependency-line" />
              Readiness dependencies and saved database connections · no traffic rate inferred
            </div>
          </div>
        </div>
        {service && (
          <Card className="topology-inspector inspector-card">
            <div className="inspector-heading">
              <Status value={selectedHealth.status} small />
              <h2>{app.service_display_names?.[name] || name}</h2>
              <Copy value={name} label="Copy service slug" />
            </div>
            {observed?.message && !selectedHealth.note && (
              <p className="inspector-summary">{observed.message}</p>
            )}
            <div className="inspector-facts">
              <div>
                <span>Replicas</span>
                <strong>{runtimeReplicaSummary(selectedHealth)}</strong>
              </div>
              <div>
                <span>Profile</span>
                <strong>{serviceProfileLabel(service)}</strong>
              </div>
              <div>
                <span>Port</span>
                <strong>{service.port || 'None'}</strong>
              </div>
              <div>
                <span>Exposure</span>
                <strong>{service.public ? 'Public' : 'Private'}</strong>
              </div>
            </div>
            <dl className="topology-details">
              <div>
                <dt>Image</dt>
                <dd>
                  <code>{observed?.image || service.image}</code>
                  <Copy value={observed?.image || service.image} label="Copy image" />
                </dd>
              </div>
              <div>
                <dt>Depends on</dt>
                <dd>{service.depends_on?.join(', ') || 'No declared dependencies'}</dd>
              </div>
              <div>
                <dt>Networks</dt>
                <dd>{service.networks?.join(', ') || 'default'}</dd>
              </div>
              {service.volume && (
                <div>
                  <dt>Persistent volume</dt>
                  <dd>
                    {service.volume.size_gib} GiB · {service.volume.mount_path}
                  </dd>
                </div>
              )}
            </dl>
            <div className="inspector-actions">
              <Link
                to="/applications/$applicationId"
                params={{ applicationId: app.id }}
                search={{ service: name }}
                className="button button-secondary"
              >
                Inspect service
                <Icon name="arrow" size={14} />
              </Link>
              <Link
                to="/applications/$applicationId"
                params={{ applicationId: app.id }}
                search={{ service: name, tab: 'logs' }}
                className="button button-secondary"
              >
                Logs
              </Link>
              {features.terminal && (
                <Link
                  to="/applications/$applicationId"
                  params={{ applicationId: app.id }}
                  search={{ service: name, tab: 'terminal' }}
                  className="button button-secondary"
                >
                  Terminal
                </Link>
              )}
            </div>
          </Card>
        )}
      </div>
      <ManagedDatabaseConnections application={app} />
      {Object.keys(app.spec.services).length > 32 && (
        <Note>
          The topology displays the first 32 services. Use the services list for the complete
          application.
        </Note>
      )}
    </div>
  )
}
