import { useState } from 'react'
import { createFileRoute, Link } from '@tanstack/react-router'
import { useQuery } from '@tanstack/react-query'
import { relative } from '../lib/api'
import { client, unwrap } from '../lib/client'
import type { Application } from '../lib/types'
import { useScope } from '../lib/scope'
import { Button } from '../components/ui/button'
import { Icon } from '../components/icons'
import { Empty, ErrorState, Loading, PageHeader, Status } from '../components/shared'
import { DeployDialog } from '../components/deploy-dialog'

export const Route = createFileRoute('/')({ component: Applications })
function Applications() {
  const scope = useScope()
  const [search, setSearch] = useState('')
  const [deployOpen, setDeployOpen] = useState(false)
  const scopeKey = `${scope.project}/${scope.environment}`
  const [page, setPage] = useState({
    scope: scopeKey,
    cursor: '',
    previous: [] as string[],
    number: 1,
  })
  const currentPage =
    page.scope === scopeKey ? page : { scope: scopeKey, cursor: '', previous: [], number: 1 }
  const query = {
    project: scope.project,
    environment: scope.environment,
    cursor: currentPage.cursor,
    limit: 25,
  }
  const applications = useQuery({
    queryKey: ['applications', scope.project, scope.environment, currentPage.cursor],
    queryFn: ({ signal }) =>
      unwrap(
        client.GET('/applications', {
          signal,
          params: { query },
        }),
      ),
    enabled: Boolean(scope.project && scope.environment),
    refetchInterval: 15000,
    gcTime: 0,
  })
  const nextCursor = applications.data?.next_cursor
  const items = applications.data?.items || []
  const filtered = items.filter((app) => app.name.toLowerCase().includes(search.toLowerCase()))
  const healthy = items.filter((app) =>
    ['healthy', 'ready'].includes(app.observed?.status || ''),
  ).length
  const services = items.reduce(
    (count, app) => count + Object.keys(app.spec?.services || {}).length,
    0,
  )
  const latest = [...items].sort((a, b) => b.updated_at.localeCompare(a.updated_at)).slice(0, 5)
  return (
    <>
      <PageHeader
        eyebrow="WORKSPACE / APPLICATIONS"
        title="A place for everything you build."
        description="From first deploy to what comes next. All on your infrastructure."
        action={
          scope.can('deployments:write') && (
            <Button variant="primary" onClick={() => setDeployOpen(true)}>
              <Icon name="plus" size={17} />
              New application
            </Button>
          )
        }
      />
      <div className="overview-stats">
        <div className="overview-stat">
          <div>
            <span>Applications</span>
            <strong>{applications.data ? items.length : '—'}</strong>
          </div>
          <span className="stat-icon">
            <Icon name="box" />
          </span>
          <p>On this page · {scope.environment}</p>
        </div>
        <div className="overview-stat">
          <div>
            <span>Healthy applications</span>
            <strong>
              {applications.data ? healthy : '—'}
              <small> / {applications.data ? items.length : '—'}</small>
            </strong>
          </div>
          <span className="stat-icon stat-green">
            <Icon name="activity" />
          </span>
          <p>Observed health on this page</p>
        </div>
        <div className="overview-stat">
          <div>
            <span>Services</span>
            <strong>{applications.data ? services : '—'}</strong>
          </div>
          <span className="stat-icon">
            <Icon name="network" />
          </span>
          <p>Across applications on this page</p>
        </div>
      </div>
      <div className="content-columns">
        <section className="application-section">
          <div className="section-toolbar">
            <div>
              <h2>
                Applications <span className="count-badge">{items.length}</span>
              </h2>
              <p>Manage services and their deployments.</p>
            </div>
            <div className="toolbar-actions">
              <div className="search-input">
                <Icon name="search" size={16} />
                <input
                  aria-label="Filter applications on this page"
                  placeholder="Filter this page…"
                  value={search}
                  onChange={(event) => setSearch(event.target.value)}
                />
              </div>
              <Button
                size="icon"
                aria-label="Refresh applications"
                onClick={() => void applications.refetch()}
              >
                <Icon name="refresh" size={15} className={applications.isFetching ? 'spin' : ''} />
              </Button>
            </div>
          </div>
          {applications.isPending ? (
            <Loading />
          ) : applications.error ? (
            <ErrorState error={applications.error} retry={() => void applications.refetch()} />
          ) : !items.length && !currentPage.cursor ? (
            <div className="application-empty">
              <Empty
                title="Good things start with a deploy."
                description="Add a container image, review your configuration, and let Hakopod take it from there."
                action={
                  scope.can('deployments:write') && (
                    <Button onClick={() => setDeployOpen(true)}>
                      <Icon name="plus" size={16} />
                      Deploy your first application
                    </Button>
                  )
                }
              />
              <div className="empty-features">
                <span>
                  <Icon name="lock" size={14} />
                  Private networking by default
                </span>
                <span>
                  <Icon name="branch" size={14} />
                  Every release recorded
                </span>
              </div>
            </div>
          ) : !filtered.length ? (
            <Empty
              icon="search"
              title="No applications found"
              description={
                search
                  ? `No application on this page matches “${search}”.`
                  : 'No applications remain on this page. Return to the first page to refresh the list.'
              }
            />
          ) : (
            <div className="application-grid">
              {filtered.map((app) => (
                <ApplicationCard key={app.id} application={app} />
              ))}
            </div>
          )}
          {(nextCursor || currentPage.cursor) && (
            <div className="list-pagination">
              <span>
                Page {currentPage.number} · {items.length} applications
              </span>
              {currentPage.cursor && (
                <Button
                  size="sm"
                  variant="ghost"
                  onClick={() => {
                    setPage({ scope: scopeKey, cursor: '', previous: [], number: 1 })
                    setSearch('')
                  }}
                >
                  First page
                </Button>
              )}
              <Button
                size="sm"
                disabled={!currentPage.previous.length || applications.isFetching}
                onClick={() => {
                  const previous = currentPage.previous.slice(0, -1)
                  setPage({
                    scope: scopeKey,
                    cursor: currentPage.previous.at(-1) || '',
                    previous,
                    number: currentPage.number - 1,
                  })
                  setSearch('')
                }}
              >
                Previous
              </Button>
              <Button
                size="sm"
                disabled={!nextCursor || applications.isFetching}
                onClick={() => {
                  if (nextCursor) {
                    setPage({
                      scope: scopeKey,
                      cursor: nextCursor,
                      previous: [...currentPage.previous, currentPage.cursor].slice(-20),
                      number: currentPage.number + 1,
                    })
                    setSearch('')
                  }
                }}
              >
                Next
                <Icon name="chevron" size={13} />
              </Button>
            </div>
          )}
        </section>
        <aside className="activity-panel">
          <div className="section-toolbar">
            <h2>Recent updates</h2>
            <Icon name="clock" size={16} />
          </div>
          {!latest.length ? (
            <div className="activity-empty">
              <div className="activity-placeholder">
                <i />
                <i />
                <i />
              </div>
              <h3>Your story starts here.</h3>
              <p>Application updates will appear as you deploy.</p>
            </div>
          ) : (
            <div className="activity-list">
              {latest.map((app) => (
                <Link
                  to="/applications/$applicationId"
                  params={{ applicationId: app.id }}
                  className="activity-item"
                  key={app.id}
                >
                  <span className="activity-mark">
                    <Icon name="branch" size={14} />
                  </span>
                  <div>
                    <strong>{app.name}</strong>
                    <p>
                      Revision {app.revision} <span>· {relative(app.updated_at)}</span>
                    </p>
                    <Status value={app.status} small />
                  </div>
                </Link>
              ))}
            </div>
          )}
          <div className="infrastructure-promo">
            <span className="promo-grid" aria-hidden="true" />
            <Icon name="server" size={25} />
            <h3>Built on your foundation.</h3>
            <p>See the machines behind your applications and their available capacity.</p>
            <Link to="/infrastructure">
              View infrastructure
              <Icon name="arrow" size={15} />
            </Link>
          </div>
        </aside>
      </div>
      <DeployDialog open={deployOpen} onOpenChange={setDeployOpen} />
    </>
  )
}

function ApplicationCard({ application: app }: { application: Application }) {
  const serviceNames = Object.keys(app.spec?.services || {})
  const url = app.observed?.services?.find((s) => s.url)?.url
  return (
    <Link
      to="/applications/$applicationId"
      params={{ applicationId: app.id }}
      className="application-card"
    >
      <div className="application-card-top">
        <div className="app-symbol">
          <Icon name="box" size={21} />
        </div>
        <Status value={app.observed?.status || 'not observed'} small />
      </div>
      <h3>
        {app.name}
        <Icon name="chevron" size={16} />
      </h3>
      <div className="card-url">
        <Icon name={url ? 'globe' : 'lock'} size={13} />
        <span>{url ? url.replace(/^https?:\/\//, '') : 'No public endpoint observed'}</span>
      </div>
      <div className="service-pills">
        {serviceNames.slice(0, 3).map((name) => (
          <span key={name}>
            <Icon name={app.spec.services[name].public ? 'globe' : 'box'} size={11} />
            {name}
          </span>
        ))}
        {serviceNames.length > 3 && <span>+{serviceNames.length - 3}</span>}
      </div>
      <div className="application-card-footer">
        <span>
          <Icon name="branch" size={13} />
          <code>r{app.revision}</code>
        </span>
        <span>Updated {relative(app.updated_at)}</span>
      </div>
    </Link>
  )
}
