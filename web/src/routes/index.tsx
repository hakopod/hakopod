import { Input } from '../components/ui/input'
import { SelectField } from '../components/ui/select'
import { lazy, Suspense, useState } from 'react'
import { createFileRoute, Link, useNavigate } from '@tanstack/react-router'
import { useQuery } from '@tanstack/react-query'
import { Badge, Tooltip } from '../components/ui/surfaces'
import { relative, timestamp } from '../lib/api'
import { client, unwrap } from '../lib/client'
import { useScope } from '../lib/scope'
import { Button } from '../components/ui/button'
import { Icon } from '../components/icons'
import { Copy, Empty, ErrorState, Loading, PageHeader, Status } from '../components/shared'
import { Brackets } from '@hakopod/hatch-ui/components/brackets'
import { Menu, MenuItem } from '@hakopod/hatch-ui/components/dropdown-menu'
import { ServiceImageIcon } from '../components/service-image-icon'

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
    <div className="ops-page application-list-page">
      <Suspense fallback={null}>
        <SampleBanner />
      </Suspense>
      <PageHeader
        title="Applications"
        action={
          scope.can('deployments:write') && (
            <div className="toolbar-actions">
              <Link
                to="/builds"
                className="button button-secondary"
                aria-label="Deploy from source"
                title="Deploy from source"
              >
                <Icon name="branch" size={14} />
                <span className="app-action-label">From source</span>
              </Link>
              <Button
                variant="primary"
                aria-label="New application"
                onClick={() => void navigate({ to: '/applications/new' })}
              >
                <Icon name="plus" size={15} />
                <span className="app-action-label">New application</span>
                <span className="app-action-short-label">New</span>
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
          <SelectField
            compact
            label="Filter application health"
            value={health}
            onValueChange={setHealth}
            options={[
              { value: 'all', label: 'All states' },
              { value: 'healthy', label: 'Healthy' },
              { value: 'attention', label: 'Needs inspection' },
            ]}
          />
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
          <div className="ops-catalog-grid" aria-label="Application cards">
            {filtered.map((app) => (
              <article key={app.id} className="ops-catalog-card interactive">
                <Brackets />
                <div className="ops-card-heading">
                  <div className="ops-card-icons" aria-hidden="true">
                    {Object.values(app.spec.services)
                      .slice(0, 3)
                      .map((service, index) => (
                        <ServiceImageIcon key={index} image={service.image} />
                      ))}
                  </div>
                  <Status value={app.observed?.status || 'not observed'} small />
                  <div className="ops-card-actions">
                    <Menu
                      trigger={
                        <Button variant="ghost" size="icon" aria-label={`Actions for ${app.name}`}>
                          <span aria-hidden="true">···</span>
                        </Button>
                      }
                    >
                      <MenuItem
                        onSelect={() =>
                          void navigate({
                            to: '/applications/$applicationId',
                            params: { applicationId: app.id },
                          })
                        }
                      >
                        <Icon name="box" size={14} />
                        Inspect application
                      </MenuItem>
                      <MenuItem
                        onSelect={() =>
                          void navigate({
                            to: '/applications/$applicationId',
                            params: { applicationId: app.id },
                            search: { tab: 'deployments' },
                          })
                        }
                      >
                        <Icon name="branch" size={14} />
                        Deployment history
                      </MenuItem>
                      {scope.can('deployments:write') && (
                        <MenuItem
                          onSelect={() =>
                            void navigate({
                              to: '/applications/$applicationId/configure',
                              params: { applicationId: app.id },
                              search: { mode: 'form' },
                            })
                          }
                        >
                          <Icon name="settings" size={14} />
                          Configure application
                        </MenuItem>
                      )}
                    </Menu>
                  </div>
                </div>
                <h2>
                  <Link
                    to="/applications/$applicationId"
                    params={{ applicationId: app.id }}
                    className="ops-card-link"
                    aria-label={`Open ${app.name}`}
                  >
                    {app.name}
                  </Link>
                </h2>
                <div className="ops-object-id">
                  <code title={app.id}>{app.id}</code>
                  <Copy value={app.id} />
                </div>
                <dl className="ops-card-facts">
                  <div>
                    <dt>Services</dt>
                    <dd>{Object.keys(app.spec.services).length}</dd>
                  </div>
                  <div>
                    <dt>Ready replicas</dt>
                    <dd>
                      {app.observed
                        ? app.observed.services?.reduce((n, service) => n + service.ready, 0) || 0
                        : 'Not observed'}
                    </dd>
                  </div>
                </dl>
                <div className="ops-card-footer">
                  <Badge>r{app.revision}</Badge>
                  <time title={app.updated_at} dateTime={app.updated_at}>
                    {relative(app.updated_at)}
                  </time>
                  <Icon name="arrow" size={15} />
                </div>
              </article>
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
    </div>
  )
}
