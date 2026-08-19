import { lazy, Suspense, useState } from 'react'
import { createFileRoute, Link, useNavigate } from '@tanstack/react-router'
import { useQuery } from '@tanstack/react-query'
import { Badge, Card, Tooltip } from '@hakopod/ui'
import { relative, timestamp } from '../lib/api'
import { client, unwrap } from '../lib/client'
import type { Application } from '../lib/types'
import { useScope } from '../lib/scope'
import { Button } from '../components/ui/button'
import { Icon } from '../components/icons'
import { Empty, ErrorState, Loading, PageHeader, Status } from '../components/shared'

const SampleBanner = lazy(() => import('../components/sample-banner'))

export const Route = createFileRoute('/')({ component: Applications })
function Applications() {
  const scope = useScope()
  const [search, setSearch] = useState('')
  const [health, setHealth] = useState('all')
  const [view, setView] = useState<'list' | 'grid'>('list')
  const [selected, setSelected] = useState('')
  const navigate = useNavigate()
  const scopeKey = `${scope.project}/${scope.environment}`
  const [page, setPage] = useState({
    scope: scopeKey,
    cursor: '',
    previous: [] as string[],
    number: 1,
  })
  const currentPage =
    page.scope === scopeKey ? page : { scope: scopeKey, cursor: '', previous: [], number: 1 }
  const applications = useQuery({
    queryKey: ['applications', scope.project, scope.environment, currentPage.cursor],
    queryFn: ({ signal }) =>
      unwrap(
        client.GET('/applications', {
          signal,
          params: {
            query: {
              project: scope.project,
              environment: scope.environment,
              cursor: currentPage.cursor,
              limit: 25,
            },
          },
        }),
      ),
    enabled: Boolean(scope.project && scope.environment),
    refetchInterval: 15000,
    gcTime: 0,
  })
  const items = applications.data?.items || []
  const healthy = items.filter((app) =>
    ['healthy', 'ready'].includes(app.observed?.status || ''),
  ).length
  const observed = items.filter((app) => Boolean(app.observed)).length
  const services = items.reduce((sum, app) => sum + Object.keys(app.spec.services).length, 0)
  const replicas = items.reduce(
    (sum, app) =>
      sum + (app.observed?.services?.reduce((count, service) => count + service.ready, 0) || 0),
    0,
  )
  const filtered = items.filter(
    (app) =>
      app.name.toLowerCase().includes(search.toLowerCase()) &&
      (health === 'all' ||
        (health === 'healthy'
          ? ['healthy', 'ready'].includes(app.observed?.status || '')
          : !['healthy', 'ready'].includes(app.observed?.status || ''))),
  )
  const inspected = filtered.find((app) => app.id === selected) || filtered[0]
  const next = applications.data?.next_cursor
  return (
    <>
      <Suspense fallback={null}>
        <SampleBanner />
      </Suspense>
      <PageHeader
        eyebrow="WORKSPACE / APPLICATIONS"
        title="Application cockpit"
        description={`${scope.project} / ${scope.environment} · Runtime state, releases, and connected services.`}
        action={
          scope.can('deployments:write') && (
            <div className="toolbar-actions">
              <Link to="/builds" className="button button-secondary">
                <Icon name="branch" size={14} />
                From source
              </Link>
              <Button variant="primary" onClick={() => void navigate({ to: '/applications/new' })}>
                <Icon name="plus" size={15} />
                New application
              </Button>
            </div>
          )
        }
      />
      <div className="cockpit-stats">
        <Metric
          label="APPLICATIONS"
          value={applications.data ? items.length : '—'}
          detail="On this page"
          icon="box"
        />
        <Metric
          label="OBSERVED HEALTHY"
          value={applications.data ? `${healthy} / ${items.length}` : '—'}
          detail={`${observed} observed · ${items.length - observed} awaiting observation`}
          icon="activity"
          tone="success"
        />
        <Metric
          label="SERVICES"
          value={applications.data ? services : '—'}
          detail="Declared in current revisions"
          icon="network"
        />
        <Metric
          label="READY REPLICAS"
          value={applications.data ? replicas : '—'}
          detail="Across observed services on this page"
          icon="server"
        />
      </div>
      <div className="cockpit-columns">
        <section className="resource-workspace">
          <div className="resource-toolbar">
            <div className="search-input">
              <Icon name="search" size={15} />
              <input
                aria-label="Filter applications on this page"
                placeholder="Find application…"
                value={search}
                onChange={(e) => setSearch(e.target.value)}
              />
            </div>
            <select
              className="compact-select"
              aria-label="Filter application health"
              value={health}
              onChange={(e) => setHealth(e.target.value)}
            >
              <option value="all">All states</option>
              <option value="healthy">Healthy</option>
              <option value="attention">Needs inspection</option>
            </select>
            <span className="form-spacer" />
            <div className="view-switch" role="group" aria-label="Application display">
              <Tooltip content="List view" side="bottom">
                <Button
                  size="icon"
                  variant="ghost"
                  aria-label="List view"
                  aria-pressed={view === 'list'}
                  onClick={() => setView('list')}
                >
                  <Icon name="menu" size={15} />
                </Button>
              </Tooltip>
              <Tooltip content="Card view" side="bottom">
                <Button
                  size="icon"
                  variant="ghost"
                  aria-label="Card view"
                  aria-pressed={view === 'grid'}
                  onClick={() => setView('grid')}
                >
                  <Icon name="grid" size={15} />
                </Button>
              </Tooltip>
            </div>
            <Tooltip content="Refresh observations" side="bottom">
              <Button
                size="icon"
                variant="ghost"
                aria-label="Refresh applications"
                onClick={() => void applications.refetch()}
              >
                <Icon name="refresh" size={15} className={applications.isFetching ? 'spin' : ''} />
              </Button>
            </Tooltip>
          </div>
          {applications.isPending ? (
            <Loading />
          ) : applications.error ? (
            <ErrorState error={applications.error} retry={() => void applications.refetch()} />
          ) : !items.length && !currentPage.cursor ? (
            <Card>
              <Empty
                title="Your next application starts here"
                description="Deploy an image, build from a repository, or start with a template."
                action={
                  <div className="toolbar-actions">
                    {scope.can('deployments:write') && (
                      <Button
                        variant="primary"
                        onClick={() => void navigate({ to: '/applications/new' })}
                      >
                        Deploy application
                      </Button>
                    )}
                    <Link to="/templates" className="button button-secondary">
                      Browse templates
                    </Link>
                  </div>
                }
              />
            </Card>
          ) : !filtered.length ? (
            <Empty
              icon="search"
              title="No matching applications"
              description="Change the name or health filter, or move to another page."
            />
          ) : view === 'list' ? (
            <div className="table-container cockpit-table">
              <table>
                <thead>
                  <tr>
                    <th>Application</th>
                    <th>Observed state</th>
                    <th>Services</th>
                    <th>Revision</th>
                    <th>Updated</th>
                  </tr>
                </thead>
                <tbody>
                  {filtered.map((app) => (
                    <tr
                      key={app.id}
                      className={inspected?.id === app.id ? 'resource-selected' : ''}
                    >
                      <td>
                        <button
                          type="button"
                          className="resource-name"
                          aria-pressed={inspected?.id === app.id}
                          onClick={() => setSelected(app.id)}
                        >
                          <span className="resource-symbol">
                            <Icon name="box" size={18} />
                          </span>
                          <span>
                            <strong>{app.name}</strong>
                            <small>
                              {app.project} / {app.environment}
                            </small>
                          </span>
                        </button>
                      </td>
                      <td>
                        <Status value={app.observed?.status || 'not observed'} small />
                      </td>
                      <td className="mono">{Object.keys(app.spec.services).length}</td>
                      <td>
                        <Badge>r{app.revision}</Badge>
                      </td>
                      <td className="muted-text">{relative(app.updated_at)}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          ) : (
            <div className="application-grid cockpit-card-grid">
              {filtered.map((app) => (
                <button
                  key={app.id}
                  type="button"
                  className={`application-card resource-card ${inspected?.id === app.id ? 'resource-selected' : ''}`}
                  aria-pressed={inspected?.id === app.id}
                  onClick={() => setSelected(app.id)}
                >
                  <div className="application-card-top">
                    <Icon name="box" size={22} />
                    <Status value={app.observed?.status || 'not observed'} small />
                  </div>
                  <h3>
                    {app.name}
                    <Icon name="chevron" size={15} />
                  </h3>
                  <div className="service-pills">
                    {Object.keys(app.spec.services)
                      .slice(0, 4)
                      .map((name) => (
                        <span key={name}>{name}</span>
                      ))}
                  </div>
                  <div className="application-card-footer">
                    <Badge>r{app.revision}</Badge>
                    <span>{relative(app.updated_at)}</span>
                  </div>
                </button>
              ))}
            </div>
          )}
          <div className="resource-footnote">
            <span>
              {items.length} applications on page {currentPage.number}
            </span>
            <span>
              {applications.dataUpdatedAt
                ? `Observed ${timestamp(new Date(applications.dataUpdatedAt).toISOString())}`
                : 'Waiting for API'}
            </span>
          </div>
          {(next || currentPage.cursor) && (
            <div className="list-pagination">
              <Button
                size="sm"
                disabled={!currentPage.previous.length || applications.isFetching}
                onClick={() =>
                  setPage({
                    scope: scopeKey,
                    cursor: currentPage.previous.at(-1) || '',
                    previous: currentPage.previous.slice(0, -1),
                    number: currentPage.number - 1,
                  })
                }
              >
                Previous
              </Button>
              <Button
                size="sm"
                disabled={!next || applications.isFetching}
                onClick={() => {
                  if (next)
                    setPage({
                      scope: scopeKey,
                      cursor: next,
                      previous: [...currentPage.previous, currentPage.cursor].slice(-20),
                      number: currentPage.number + 1,
                    })
                }}
              >
                Next
                <Icon name="chevron" size={13} />
              </Button>
            </div>
          )}
        </section>
        <aside className="resource-inspector" aria-label="Application inspector">
          {inspected ? (
            <ApplicationInspector app={inspected} />
          ) : (
            <Card className="inspector-empty">
              <Icon name="box" size={26} />
              <h2>Resource inspector</h2>
              <p>
                Select an application to inspect its observed state, services, and last release.
              </p>
              <Link to="/infrastructure" className="button button-secondary">
                Inspect infrastructure
                <Icon name="arrow" size={14} />
              </Link>
            </Card>
          )}
        </aside>
      </div>
    </>
  )
}
function Metric({
  label,
  value,
  detail,
  icon,
  tone,
}: {
  label: string
  value: number | string
  detail: string
  icon: string
  tone?: string
}) {
  return (
    <Card className={`cockpit-metric ${tone || ''}`}>
      <span className="metric-label">
        <Icon name={icon} size={13} />
        {label}
      </span>
      <strong>{value}</strong>
      <small>{detail}</small>
    </Card>
  )
}
function ApplicationInspector({ app }: { app: Application }) {
  const observed = app.observed?.services || []
  return (
    <Card className="inspector-card">
      <div className="inspector-heading">
        <Icon name="box" size={20} />
        <h2>{app.name}</h2>
        <Status value={app.observed?.status || 'not observed'} small />
      </div>
      <p className="inspector-summary">
        {observed.find((service) => service.message)?.message ||
          'Inspect services for current pod health, resource usage, and Kubernetes events.'}
      </p>
      <div className="inspector-facts">
        <div>
          <span>Revision</span>
          <strong>r{app.revision}</strong>
        </div>
        <div>
          <span>Release state</span>
          <strong>{app.status}</strong>
        </div>
        <div>
          <span>Services</span>
          <strong>{Object.keys(app.spec.services).length}</strong>
        </div>
        <div>
          <span>Environment</span>
          <strong>{app.environment}</strong>
        </div>
      </div>
      <div className="eyebrow">SERVICES</div>
      <div className="inspector-services">
        {Object.entries(app.spec.services).map(([name, service]) => {
          const state = observed.find((row) => row.name === name)
          return (
            <Link
              key={name}
              to="/applications/$applicationId"
              params={{ applicationId: app.id }}
              search={{ service: name }}
              className="inspector-service"
            >
              <Icon name={service.public ? 'globe' : service.port ? 'box' : 'terminal'} size={15} />
              <span>
                <strong>{name}</strong>
                <small>
                  {state
                    ? `${state.ready}/${state.desired} replicas ready`
                    : 'Awaiting observation'}
                </small>
              </span>
              <Status value={state?.status || 'unknown'} small />
            </Link>
          )
        })}
      </div>
      <div className="inspector-actions">
        <Link
          to="/applications/$applicationId"
          params={{ applicationId: app.id }}
          className="button button-primary"
        >
          Open application
          <Icon name="arrow" size={14} />
        </Link>
        <Link
          to="/applications/$applicationId"
          params={{ applicationId: app.id }}
          search={{ tab: 'logs' }}
          className="button button-secondary"
        >
          <Icon name="terminal" size={14} />
          Open logs
        </Link>
      </div>
      <div className="inspector-footer">Updated {timestamp(app.updated_at)}</div>
    </Card>
  )
}
