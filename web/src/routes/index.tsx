import { Input } from '../components/ui/input'
import { Select } from '../components/ui/select'
import { lazy, Suspense, useState } from 'react'
import { createFileRoute, Link, useNavigate } from '@tanstack/react-router'
import { useQuery } from '@tanstack/react-query'
import { Badge, Tooltip } from '../components/ui/surfaces'
import { Brackets } from '@hakopod/hatch-ui/components/brackets'
import { relative, timestamp } from '../lib/api'
import { client, unwrap } from '../lib/client'
import { useScope } from '../lib/scope'
import { Button } from '../components/ui/button'
import { Icon } from '../components/icons'
import { Copy, Empty, ErrorState, Loading, PageHeader, Status } from '../components/shared'

const SampleBanner = lazy(() => import('../components/sample-banner'))

export const Route = createFileRoute('/')({ component: Applications })
function Applications() {
  const scope = useScope()
  const [search, setSearch] = useState('')
  const [health, setHealth] = useState('all')
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
  const next = applications.data?.next_cursor
  return (
    <div className="ops-page">
      <Suspense fallback={null}>
        <SampleBanner />
      </Suspense>
      <PageHeader
        eyebrow="WORKSPACE / APPLICATIONS"
        title="Applications"
        description={`${scope.project} / ${scope.environment} · Observed workloads and current revisions.`}
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
      <dl className="ops-summary" aria-label="Application summary on this page">
        <div>
          <dt>Applications</dt>
          <dd>{applications.data ? items.length : '—'}</dd>
          <small>On this page</small>
        </div>
        <div>
          <dt>Healthy</dt>
          <dd>{applications.data ? `${healthy} / ${items.length}` : '—'}</dd>
          <small>{observed} observed</small>
        </div>
        <div>
          <dt>Services</dt>
          <dd>{applications.data ? services : '—'}</dd>
          <small>In current revisions</small>
        </div>
        <div>
          <dt>Ready replicas</dt>
          <dd>{applications.data ? replicas : '—'}</dd>
          <small>Observed services</small>
        </div>
      </dl>
      <section className="ops-resource-list" aria-label="Applications">
        <div className="resource-toolbar">
          <div className="ops-search-input">
            <Icon name="search" size={15} />
            <Input
              aria-label="Filter applications on this page"
              placeholder="Find application…"
              value={search}
              onChange={(e) => setSearch(e.target.value)}
            />
          </div>
          <Select
            className="compact-select"
            aria-label="Filter application health"
            value={health}
            onChange={(e) => setHealth(e.target.value)}
          >
            <option value="all">All states</option>
            <option value="healthy">Healthy</option>
            <option value="attention">Needs inspection</option>
          </Select>
          <span className="form-spacer" />
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
          <div className="ops-empty-grid">
            <Empty
              title="Your next application starts here"
              description="Deploy an image, build from a repository, or start with a template."
              action={
                <div className="toolbar-actions">
                  {scope.can('deployments:write') && (
                    <Button onClick={() => void navigate({ to: '/applications/new' })}>
                      Deploy application
                    </Button>
                  )}
                  <Link to="/templates" className="button button-secondary">
                    Browse templates
                  </Link>
                </div>
              }
            />
          </div>
        ) : !filtered.length ? (
          <Empty
            icon="search"
            title="No matching applications"
            description="Change the name or health filter, or move to another page."
          />
        ) : (
          <div className="table-container ops-table">
            <table>
              <thead>
                <tr>
                  <th>Application</th>
                  <th>Services / replicas</th>
                  <th>Revision</th>
                  <th>Updated</th>
                  <th>
                    <span className="sr-only">Open</span>
                  </th>
                </tr>
              </thead>
              <tbody>
                {filtered.map((app) => (
                  <tr key={app.id}>
                    <td>
                      <div className="ops-object">
                        <Status value={app.observed?.status || 'not observed'} small />
                        <Link
                          to="/applications/$applicationId"
                          params={{ applicationId: app.id }}
                          className="ops-object-name interactive"
                        >
                          <Brackets />
                          {app.name}
                        </Link>
                      </div>
                      <div className="ops-object-id">
                        <code>{app.id}</code>
                        <Copy value={app.id} />
                      </div>
                    </td>
                    <td>
                      <span className="mono">{Object.keys(app.spec.services).length} services</span>
                      <small className="ops-table-sub">
                        {app.observed
                          ? `${app.observed.services?.reduce((n, service) => n + service.ready, 0) || 0} ready replicas`
                          : 'Awaiting observation'}
                      </small>
                    </td>
                    <td>
                      <Badge>r{app.revision}</Badge>
                    </td>
                    <td>
                      <time title={app.updated_at} dateTime={app.updated_at}>
                        {relative(app.updated_at)}
                      </time>
                    </td>
                    <td>
                      <Button asChild size="icon" variant="ghost">
                        <Link
                          to="/applications/$applicationId"
                          params={{ applicationId: app.id }}
                          aria-label={`Open ${app.name}`}
                        >
                          <Icon name="chevron" size={16} />
                        </Link>
                      </Button>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
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
    </div>
  )
}
