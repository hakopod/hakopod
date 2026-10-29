import { RetainedStorage } from './retained-storage'
import { RenameResource } from './rename-resource'
import { Pencil } from 'lucide-react'
import { useEditionFeatures } from '../lib/dashboard-edition'
import { Input } from './ui/input'
import { SelectField } from './ui/select'
import { lazy, Suspense, useEffect, useState } from 'react'
import { Link, useNavigate } from '@tanstack/react-router'
import { useQuery } from '@tanstack/react-query'
import { Tooltip } from './ui/surfaces'
import { relative, timestamp } from '../lib/api'
import { client, unwrap } from '../lib/client'
import { ScopeContext, canAccess, useScope } from '../lib/scope'
import type { Application, Project } from '../lib/types'
import { applicationRuntimeHealth } from '../lib/runtime-health'
import { Button } from './ui/button'
import { Icon } from './icons'
import { Loading, PageHeader } from './shared'
import { publicEndpoints } from '../lib/public-endpoints'
import {
  CardMetric,
  ExternalEndpoint,
  ObservedState,
  ResourceCard,
  GuidedEmpty,
  ResourceEmpty,
  ResourceError,
  StaleNotice,
} from './resource-card'
import { Menu, MenuItem } from '@hakopod/hatch-ui/components/dropdown-menu'
import { ServiceImageIcon } from './service-image-icon'
import { DeleteResource } from './delete-resource'

const SampleBanner = lazy(() => import('./sample-banner'))

export function ApplicationList({
  project,
  environment,
}: {
  project: Project
  environment: string
}) {
  const features = useEditionFeatures()
  const workspace = useScope()
  const scope = {
    ...workspace,
    project: project.name,
    environment,
    can: (permission: string) => canAccess(workspace.identity, project.name, permission),
  }
  useEffect(() => {
    workspace.syncScope(project.name, environment)
  }, [workspace.syncScope, project.name, environment])
  const [renameApplication, setRenameApplication] = useState<Application | null>(null)
  const [deleteApplication, setDeleteApplication] = useState<Application | null>(null)
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
  const healthByID = new Map(items.map((app) => [app.id, applicationRuntimeHealth(app)]))
  const healthy = items.filter((app) => healthByID.get(app.id)?.status === 'healthy').length
  const observed = items.filter((app) => healthByID.get(app.id)?.ready !== undefined).length
  const services = items.reduce((sum, app) => sum + Object.keys(app.spec.services).length, 0)
  const replicas = items.reduce((sum, app) => sum + (healthByID.get(app.id)?.ready || 0), 0)
  const filtered = items.filter(
    (app) =>
      `${app.display_name || ''} ${app.name}`.toLowerCase().includes(search.toLowerCase()) &&
      (health === 'all' ||
        (health === 'healthy'
          ? healthByID.get(app.id)?.status === 'healthy'
          : healthByID.get(app.id)?.status !== 'healthy')),
  )
  const next = applications.data?.next_cursor
  return (
    <ScopeContext.Provider value={scope}>
      <div className="ops-page application-list-page">
        {renameApplication && (
          <RenameResource
            application={renameApplication}
            trigger="none"
            initiallyOpen
            onClose={() => setRenameApplication(null)}
            onCloseAutoFocus={(event) => {
              event.preventDefault()
              document.getElementById(`application-actions-${renameApplication.id}`)?.focus()
            }}
          />
        )}
        {deleteApplication && (
          <DeleteResource
            key={deleteApplication.id}
            application={deleteApplication}
            project={deleteApplication.project}
            trigger="none"
            initiallyOpen
            onClose={() => setDeleteApplication(null)}
            onCloseAutoFocus={(event) => {
              event.preventDefault()
              document.getElementById(`application-actions-${deleteApplication.id}`)?.focus()
            }}
          />
        )}

        <Suspense fallback={null}>
          <SampleBanner />
        </Suspense>
        <PageHeader
          title="Applications"
          description={`Applications in ${project.display_name || project.name} / ${environment}. Health comes from the latest runtime observation.`}
          action={
            scope.can('deployments:write') && (
              <div className="toolbar-actions">
                <Button asChild>
                  <Link
                    to="/projects/$project"
                    params={{ project: project.name }}
                    search={{ environment, tab: 'manage' }}
                  >
                    Project settings
                  </Link>
                </Button>
                {features.git && (
                  <Button asChild>
                    <Link to="/builds" aria-label="Deploy from source" title="Deploy from source">
                      <Icon name="branch" size={14} />
                      <span className="app-action-label">From source</span>
                    </Link>
                  </Button>
                )}
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
        <dl className="application-summary" aria-label="Application summary on this page">
          <div title="Applications on this page">
            <dt>Applications</dt>
            <dd>{applications.data ? items.length : '—'}</dd>
          </div>
          <div title={`${observed} applications observed on this page`}>
            <dt>Healthy</dt>
            <dd>{applications.data && observed ? `${healthy} / ${items.length}` : '—'}</dd>
          </div>
          <div title="Services in current application revisions on this page">
            <dt>Services</dt>
            <dd>{applications.data ? services : '—'}</dd>
          </div>
          <div title="Ready replicas in observed services on this page">
            <dt>Ready replicas</dt>
            <dd>{applications.data && observed ? replicas : '—'}</dd>
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
          ) : applications.error && !applications.data ? (
            <ResourceError
              error={applications.error}
              retry={() => void applications.refetch()}
              subject="applications in this environment"
            />
          ) : !items.length && !currentPage.cursor && scope.can('deployments:write') ? (
            <GuidedEmpty
              icon="box"
              satellites={[
                { icon: 'branch', label: 'Repository' },
                { icon: 'globe', label: 'Address' },
                { icon: 'code', label: 'Compose' },
                { icon: 'database', label: 'Data' },
              ]}
              title={`Deploy your first application to ${scope.environment}`}
              description="Start from a repository, a container image or a Compose file. Hakopod builds it, rolls it out and gives each public service an address."
              primary={
                <Button
                  variant="primary"
                  onClick={() => void navigate({ to: '/applications/new' })}
                >
                  <Icon name="plus" size={14} />
                  New application
                </Button>
              }
              secondary={
                <Button asChild>
                  <Link to="/templates">Browse the catalog</Link>
                </Button>
              }
              tip="Every change is planned and shown to you before anything goes live."
              steps={[
                {
                  title: 'Choose a source',
                  detail: 'A Git repository, an image or a Compose file.',
                },
                {
                  title: 'Review the plan',
                  detail: 'See each service, its size and the exact configuration changes.',
                },
                {
                  title: 'Watch it roll out',
                  detail: 'Follow each service until it is ready, with logs one click away.',
                },
              ]}
            />
          ) : !items.length && !currentPage.cursor ? (
            <ResourceEmpty
              title="No applications in this environment"
              description="Applications appear here once someone with deploy access creates one."
            />
          ) : !filtered.length ? (
            <ResourceEmpty
              icon="search"
              title="No matching applications"
              description="Change the name or health filter, or move to another page."
            />
          ) : (
            <>
              {applications.error && (
                <StaleNotice
                  updatedAt={applications.dataUpdatedAt}
                  retry={() => void applications.refetch()}
                />
              )}
              <div className="resource-grid" aria-label="Application cards">
                {filtered.map((app) => {
                  const appHealth = healthByID.get(app.id)!
                  const serviceCount = Object.keys(app.spec.services).length
                  const endpoints = publicEndpoints(app)
                  const issue = appHealth.issues[0]
                  return (
                    <ResourceCard
                      key={app.id}
                      cue={Object.values(app.spec.services)
                        .slice(0, 3)
                        .map((service, index) => (
                          <ServiceImageIcon
                            key={index}
                            image={service.image}
                            actions={service.actions}
                          />
                        ))}
                      name={
                        <Link
                          to="/applications/$applicationId"
                          params={{ applicationId: app.id }}
                          className="ops-card-link"
                          aria-label={`Open ${app.display_name || app.name}`}
                        >
                          {app.display_name || app.name}
                        </Link>
                      }
                      menu={
                        <Menu
                          trigger={
                            <Button
                              variant="ghost"
                              size="icon"
                              aria-label={`Actions for ${app.display_name || app.name}`}
                              id={`application-actions-${app.id}`}
                            >
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
                          {(scope.identity.admin ||
                            scope.identity.can_manage_applications ||
                            scope.identity.project_roles?.some(
                              (role) => role.project === app.project && role.role === 'admin',
                            )) && (
                            <>
                              <MenuItem onSelect={() => setRenameApplication(app)}>
                                <Pencil size={14} />
                                Rename application
                              </MenuItem>
                              <MenuItem destructive onSelect={() => setDeleteApplication(app)}>
                                <Icon name="trash" size={14} />
                                Delete application
                              </MenuItem>
                            </>
                          )}
                        </Menu>
                      }
                      meta={
                        <>
                          Application · {serviceCount} {serviceCount === 1 ? 'service' : 'services'}{' '}
                          · r{app.revision} · updated{' '}
                          <time title={app.updated_at} dateTime={app.updated_at}>
                            {relative(app.updated_at).toLowerCase()}
                          </time>
                        </>
                      }
                      endpoint={
                        endpoints[0] && (
                          <>
                            <ExternalEndpoint url={endpoints[0].url} label={endpoints[0].service} />
                            {endpoints.length > 1 && <span>+{endpoints.length - 1}</span>}
                          </>
                        )
                      }
                      state={
                        <ObservedState
                          status={appHealth.status}
                          observedAt={appHealth.observedAt}
                          note={issue ? `${issue.service}: ${issue.message}` : appHealth.note}
                        />
                      }
                      metric={
                        <CardMetric
                          label="Ready replicas"
                          window="now"
                          value={
                            appHealth.ready === undefined
                              ? undefined
                              : `${appHealth.ready} / ${appHealth.desired}`
                          }
                        />
                      }
                    />
                  )
                })}
              </div>
            </>
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
        <RetainedStorage project={project.name} environment={environment} />
      </div>
    </ScopeContext.Provider>
  )
}
