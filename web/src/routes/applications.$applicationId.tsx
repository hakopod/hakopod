import { ApplicationSecurity } from '../components/application-security'
import { ManagedActionsStatus } from '../components/managed-actions-status'
import { VolumeResizeOperations } from '../components/volume-resize'
import { publicEndpoints } from '../lib/public-endpoints'
import { PublicEndpoints } from '../components/public-endpoints'
import { serviceProfileLabel } from '../lib/service-resources'
import { MoveServiceDialog, ServiceMoves } from '../components/move-service-dialog'
import { RenameResource } from '../components/rename-resource'
import { ApplicationPreviews } from '../components/application-previews'
import { ServicePowerDialog } from '../components/service-power-dialog'
import { useEditionFeatures } from '../lib/dashboard-edition'
import { DeleteServiceDialog } from '../components/delete-service-dialog'
import { DeleteResource } from '../components/delete-resource'
import { lazy, Suspense, useEffect, useMemo, useRef, useState } from 'react'
import { createFileRoute, Link, useNavigate, useLocation, Outlet } from '@tanstack/react-router'
import { useQueries, useQuery } from '@tanstack/react-query'
import * as Tabs from '@radix-ui/react-tabs'
import { relative } from '../lib/api'
import { client, unwrap } from '../lib/client'
import { downloadConfig, specToTOML } from '../lib/toml'
import { useScope } from '../lib/scope'
import { useActiveSection } from '../lib/use-active-section'
import { applicationRuntimeHealth, runtimeReplicaSummary } from '../lib/runtime-health'
import {
  ActivityList,
  HealthLine,
  NeedsAttention,
  rankedServices,
  type InspectTab,
} from '../components/application-workspace'
import { ServiceInspector } from '../components/service-inspector'
import { Icon } from '../components/icons'
import { Button } from '../components/ui/button'
import {
  CardMetric,
  ExternalEndpoint,
  ObservedState,
  ResourceCard,
  ResourceEmpty,
  ResourceRow,
  ResourceRows,
} from '../components/resource-card'
import { ServiceImageIcon } from '../components/service-image-icon'
import { Menu, MenuItem } from '@hakopod/hatch-ui/components/dropdown-menu'
import { HeadingHelp, Copy, ErrorState, Loading, Note, Status } from '../components/shared'
import { TOMLCode } from '../components/toml-code'
const ServiceDetail = lazy(() =>
  import('../components/service-detail').then((m) => ({ default: m.ServiceDetail })),
)
const SampleBanner = lazy(() => import('../components/sample-banner'))
const ApplicationTopology = lazy(() => import('../components/application-topology'))
const ApplicationSecrets = lazy(() => import('../components/application-secrets'))
const ApplicationSource = lazy(() => import('../components/application-source'))
const IssuerSettings = lazy(() => import('../components/tls-settings'))

export const Route = createFileRoute('/applications/$applicationId')({
  validateSearch: (
    search: Record<string, unknown>,
  ): {
    service?: string
    tab?: string
    pod?: string
    run?: string
    job?: string
    runner?: string
    inspect?: string
    itab?: string
  } => ({
    service: typeof search.service === 'string' ? search.service : undefined,
    tab:
      typeof search.tab === 'string' &&
      [
        'actions',
        'topology',
        'services',
        'deployments',
        'previews',
        'logs',
        'requests',
        'terminal',
        'networking',
        'configuration',
        'source',
        'secrets',
        'environment',
        'pods',
        'overview',
        'network',
        'settings',
        'activity',
        'details',
      ].includes(search.tab)
        ? search.tab
        : undefined,
    run: typeof search.run === 'string' ? search.run.slice(0, 256) : undefined,
    job: typeof search.job === 'string' ? search.job.slice(0, 64) : undefined,
    runner: typeof search.runner === 'string' ? search.runner.slice(0, 64) : undefined,
    pod: typeof search.pod === 'string' ? search.pod : undefined,
    inspect: typeof search.inspect === 'string' ? search.inspect.slice(0, 128) : undefined,
    itab:
      typeof search.itab === 'string' && ['logs', 'shell', 'config'].includes(search.itab)
        ? search.itab
        : undefined,
  }),
  component: ApplicationRoute,
})
function ApplicationRoute() {
  const { applicationId } = Route.useParams()
  return useLocation().pathname === `/applications/${applicationId}` ? (
    <ApplicationDetail />
  ) : (
    <Outlet />
  )
}
const applicationTabs = ['overview', 'activity', 'settings'] as const
const settingsSections = [
  'details',
  'configuration',
  'source',
  'secrets',
  'networking',
  'previews',
] as const
type SettingsSection = (typeof settingsSections)[number]
type View = { tab: string; section: SettingsSection; graph: boolean }

// Older links used one tab per task; map them onto the three sections.
function initialView(tab?: string): View {
  const base: View = { tab: 'overview', section: 'details', graph: false }
  if (!tab) return base
  if ((settingsSections as readonly string[]).includes(tab))
    return { ...base, tab: 'settings', section: tab as SettingsSection }
  if (tab === 'deployments' || tab === 'activity') return { ...base, tab: 'activity' }
  if (tab === 'topology') return { ...base, graph: true }
  if ((applicationTabs as readonly string[]).includes(tab)) return { ...base, tab }
  return base
}

// "docker.io/library/python@sha256:…" reads as "python" with a pinned marker; the
// full reference stays in the title and the service inspector.
function imageLabel(image: string) {
  const [reference, digest] = image.split('@')
  const name = (reference.split('/').pop() || reference).split(':')[0]
  return { name, pinned: Boolean(digest) }
}

const inspectTabs: InspectTab[] = ['overview', 'logs', 'shell', 'config']

function ApplicationDetail() {
  const { applicationId } = Route.useParams()
  const {
    service: selectedService,
    tab: selectedTab,
    pod: selectedPod,
    inspect,
    itab,
  } = Route.useSearch()
  const features = useEditionFeatures()
  const scope = useScope()
  const navigate = useNavigate()
  const [view, setView] = useState(() => initialView(selectedTab))
  const navigationRoot = useActiveSection(view.tab, '.tab-list')
  useEffect(() => {
    if (selectedTab && !selectedService) setView(initialView(selectedTab))
  }, [selectedTab, selectedService])
  const [powerService, setPowerService] = useState('')
  const [deletingService, setDeletingService] = useState('')
  const [movingService, setMovingService] = useState('')
  const serviceMenuTriggers = useRef<Record<string, HTMLButtonElement | null>>({})
  const application = useQuery({
    queryKey: ['application', applicationId],
    queryFn: ({ signal }) =>
      unwrap(client.GET('/applications/{id}', { signal, params: { path: { id: applicationId } } })),
    refetchInterval: 10000,
    refetchIntervalInBackground: false,
    gcTime: 0,
  })
  const domains = useQuery({
    queryKey: ['application-domains', applicationId],
    queryFn: ({ signal }) =>
      unwrap(
        client.GET('/applications/{id}/domains', {
          signal,
          params: { path: { id: applicationId } },
        }),
      ),
    enabled: !!application.data && Object.keys(application.data.spec.domains || {}).length > 0,
    refetchInterval: 15000,
    refetchIntervalInBackground: false,
  })
  const serviceKeys = application.data ? Object.keys(application.data.spec.services) : []
  // CPU and restarts per service, so each card can show something worth reading.
  const runtimes = useQueries({
    queries: serviceKeys.map((name) => ({
      queryKey: ['service-runtime', applicationId, name],
      queryFn: ({ signal }: { signal: AbortSignal }) =>
        unwrap(
          client.GET('/applications/{id}/services/{service}/runtime', {
            signal,
            params: { path: { id: applicationId, service: name } },
          }),
        ),
      refetchInterval: 30000,
      refetchIntervalInBackground: false,
      enabled: !selectedService,
    })),
  })
  const configuration = useMemo(
    () => (application.data ? specToTOML(application.data.spec) : ''),
    [application.data?.spec],
  )
  useEffect(() => {
    if (application.data) scope.syncScope(application.data.project, application.data.environment)
  }, [application.data?.project, application.data?.environment, scope.syncScope])
  // Logs and the terminal belong to a service: send older application-level links to its inspector.
  const firstService = serviceKeys[0] || ''
  useEffect(() => {
    if (!selectedService && firstService && (selectedTab === 'logs' || selectedTab === 'terminal'))
      void navigate({
        to: '/applications/$applicationId',
        params: { applicationId },
        search: { inspect: firstService, itab: selectedTab === 'logs' ? 'logs' : 'shell' },
        replace: true,
      })
  }, [selectedService, selectedTab, firstService])
  if (application.isPending) return <Loading rows={4} />
  if (application.error || !application.data)
    return <ErrorState error={application.error} retry={() => void application.refetch()} />
  const app = application.data
  const health = applicationRuntimeHealth(app)
  const serviceNames = Object.keys(app.spec.services)
  const observed = app.observed?.services || []
  const endpoints = publicEndpoints(
    app,
    domains.data?.items,
    domains.isError ? 'error' : domains.isPending ? 'loading' : 'ready',
  )
  const canDeploy = scope.can('deployments:write')
  const inspectService = (name: string, tab: InspectTab = 'overview') =>
    void navigate({
      to: '/applications/$applicationId',
      params: { applicationId: app.id },
      search: { inspect: name, ...(tab !== 'overview' ? { itab: tab } : {}) },
    })
  const closeInspector = () =>
    void navigate({
      to: '/applications/$applicationId',
      params: { applicationId: app.id },
      search: {},
    })
  const openSettings = (section: SettingsSection) =>
    setView((current) => ({ ...current, tab: 'settings', section }))
  if (selectedService)
    return (
      <Suspense fallback={<Loading />}>
        <ServiceDetail
          key={`${app.id}:${selectedService}`}
          application={app}
          endpoints={endpoints}
          serviceName={selectedService}
          initialTab={selectedTab}
          initialPod={selectedPod}
        />
      </Suspense>
    )
  const runtimeFor = (name: string) => runtimes[serviceKeys.indexOf(name)]?.data
  const networks = [
    ...new Set(Object.values(app.spec.services).flatMap((s) => s.networks || ['default'])),
  ]
  const sections: [SettingsSection, string][] = [
    ['details', 'Details'],
    ['configuration', 'Configuration'],
    ...(features.git ? ([['source', 'Source']] as [SettingsSection, string][]) : []),
    ['secrets', 'Secrets'],
    ['networking', 'Networking and TLS'],
    ['previews', 'Preview environments'],
  ]
  return (
    <div className="ops-page app-workspace">
      {inspect && app.spec.services[inspect] && (
        <ServiceInspector
          application={app}
          serviceName={inspect}
          tab={inspectTabs.includes(itab as InspectTab) ? (itab as InspectTab) : 'overview'}
          onTabChange={(tab) =>
            void navigate({
              to: '/applications/$applicationId',
              params: { applicationId: app.id },
              search: { inspect, ...(tab !== 'overview' ? { itab: tab } : {}) },
              replace: true,
            })
          }
          onClose={closeInspector}
        />
      )}
      {movingService && (
        <MoveServiceDialog
          application={app}
          service={movingService}
          restoreFocus={() => serviceMenuTriggers.current[movingService]?.focus()}
          onClose={() => setMovingService('')}
        />
      )}
      {powerService && (
        <ServicePowerDialog
          application={app}
          service={powerService}
          onClose={() => setPowerService('')}
        />
      )}
      {deletingService && (
        <DeleteServiceDialog
          application={app}
          service={deletingService}
          onClose={() => setDeletingService('')}
        />
      )}
      <header className="app-workspace-head">
        <div className="app-workspace-identity">
          <nav className="app-workspace-scope" aria-label="Application scope">
            <Link to="/projects/$project" params={{ project: app.project }}>
              {app.project}
            </Link>
            <span aria-hidden="true">/</span>
            <span>{app.environment}</span>
          </nav>
          <div className="app-workspace-title">
            <h1>{app.display_name || app.name}</h1>
            <RenameResource application={app} />
          </div>
          <div className="app-workspace-meta">
            <HealthLine app={app} />
            <span>Revision {app.revision}</span>
            <span>Updated {relative(app.updated_at)}</span>
          </div>
          <PublicEndpoints endpoints={endpoints} compact />
        </div>
        <div className="app-workspace-actions">
          {canDeploy && (
            <Button asChild>
              <Link to="/applications/$applicationId/services/new" params={{ applicationId }}>
                <Icon name="plus" size={16} />
                Add service
              </Link>
            </Button>
          )}
          {canDeploy && (
            <Button
              variant="primary"
              onClick={() =>
                void navigate({
                  to: '/applications/$applicationId/configure',
                  params: { applicationId },
                  search: { mode: 'form' },
                })
              }
            >
              Deploy changes
            </Button>
          )}
          <Menu
            trigger={
              <Button variant="ghost" size="icon" aria-label="More application actions">
                <span aria-hidden="true">···</span>
              </Button>
            }
          >
            <MenuItem onSelect={() => void navigator.clipboard?.writeText(app.id)}>
              <Icon name="copy" size={14} />
              Copy application ID
            </MenuItem>
            {canDeploy && (
              <MenuItem
                onSelect={() =>
                  void navigate({
                    to: '/applications/$applicationId/environment',
                    params: { applicationId },
                  })
                }
              >
                <Icon name="code" size={14} />
                Environment variables
              </MenuItem>
            )}
            <MenuItem onSelect={() => downloadConfig(app.spec)}>
              <Icon name="code" size={14} />
              Export TOML
            </MenuItem>
            <MenuItem
              onSelect={() =>
                void navigate({
                  to: '/applications/$applicationId/notifications',
                  params: { applicationId },
                })
              }
            >
              <Icon name="activity" size={14} />
              Deployment notifications
            </MenuItem>
            <MenuItem onSelect={() => openSettings('details')}>
              <Icon name="settings" size={14} />
              Application details
            </MenuItem>
          </Menu>
        </div>
      </header>
      {domains.data?.items.some(
        (domain) => !!app.spec.domains?.[domain.hostname] && !domain.active,
      ) && (
        <Note>
          Custom domains need setup.{' '}
          <Link to="/applications/$applicationId/domains" params={{ applicationId }}>
            Configure DNS and activate domains
          </Link>
        </Note>
      )}
      <Suspense fallback={null}>
        <SampleBanner applicationId={app.id} />
      </Suspense>
      <Tabs.Root
        value={view.tab}
        onValueChange={(tab) => setView((current) => ({ ...current, tab }))}
      >
        <Tabs.List
          ref={navigationRoot}
          className="tab-list application-tabs"
          aria-label="Application sections"
        >
          {[
            ['overview', 'Overview'],
            ['activity', 'Activity'],
            ['settings', 'Settings'],
          ].map(([value, label]) => (
            <Tabs.Trigger className="tab-trigger" key={value} value={value}>
              {label}
            </Tabs.Trigger>
          ))}
        </Tabs.List>
        <Tabs.Content value="overview" className="tab-content app-workspace-overview">
          <NeedsAttention app={app} onInspect={inspectService} />
          <ApplicationSecurity applicationId={app.id} attentionOnly />
          <VolumeResizeOperations application={app} />
          {canDeploy && <ServiceMoves application={app} />}
          <ManagedActionsStatus application={app} />
          <section className="app-workspace-section" aria-labelledby="app-services-heading">
            <div className="app-workspace-section-head">
              <h2 id="app-services-heading">Services</h2>
              <span className="app-workspace-count">{serviceNames.length}</span>
              {serviceNames.length > 1 && (
                <div className="app-view-toggle" role="group" aria-label="Services view">
                  <button
                    type="button"
                    aria-pressed={!view.graph}
                    onClick={() => setView((current) => ({ ...current, graph: false }))}
                  >
                    Cards
                  </button>
                  <button
                    type="button"
                    aria-pressed={view.graph}
                    onClick={() => setView((current) => ({ ...current, graph: true }))}
                  >
                    Graph
                  </button>
                </div>
              )}
            </div>
            {!serviceNames.length ? (
              <ResourceEmpty
                icon="box"
                title="No services"
                description="Add a service to run code or an image in this application."
                action={
                  canDeploy && (
                    <Button asChild>
                      <Link
                        to="/applications/$applicationId/services/new"
                        params={{ applicationId }}
                      >
                        Add service
                      </Link>
                    </Button>
                  )
                }
              />
            ) : view.graph ? (
              <Suspense fallback={<Loading />}>
                <ApplicationTopology application={app} />
              </Suspense>
            ) : (
              <div className="resource-grid" aria-label="Service cards">
                {rankedServices(app).map(({ name, runtime, health: serviceHealth }) => {
                  const service = app.spec.services[name]
                  const label = app.service_display_names?.[name] || name
                  const image = runtime?.image || service.image
                  const shown = imageLabel(image)
                  const replicas = runtimeReplicaSummary(serviceHealth, Boolean(service.job))
                  const address =
                    runtime?.internal_address || (service.port ? `${name}:${service.port}` : '')
                  const live = runtimeFor(name)
                  const restarts = (live?.pods || []).reduce(
                    (total, pod) =>
                      total +
                      (pod.containers || []).reduce(
                        (n, container) => n + (container.restarts || 0),
                        0,
                      ),
                    0,
                  )
                  const metrics = live?.metrics
                  return (
                    <ResourceCard
                      key={name}
                      cue={<ServiceImageIcon image={image} actions={service.actions} />}
                      name={
                        <Link
                          className="ops-card-link"
                          to="/applications/$applicationId"
                          params={{ applicationId: app.id }}
                          search={{ inspect: name }}
                          aria-label={`Inspect ${label} service`}
                        >
                          {label}
                        </Link>
                      }
                      menu={
                        <Menu
                          trigger={
                            <Button
                              variant="ghost"
                              size="icon"
                              ref={(element) => {
                                serviceMenuTriggers.current[name] = element
                              }}
                              aria-label={`Actions for ${label}`}
                            >
                              <span aria-hidden="true">···</span>
                            </Button>
                          }
                        >
                          <MenuItem onSelect={() => inspectService(name)}>
                            <Icon name="box" size={14} />
                            Inspect service
                          </MenuItem>
                          {scope.can('logs:read') && (
                            <MenuItem onSelect={() => inspectService(name, 'logs')}>
                              <Icon name="activity" size={14} />
                              View logs
                            </MenuItem>
                          )}
                          {features.terminal && (
                            <MenuItem onSelect={() => inspectService(name, 'shell')}>
                              <Icon name="terminal" size={14} />
                              Open shell
                            </MenuItem>
                          )}
                          {canDeploy && (
                            <MenuItem
                              onSelect={() =>
                                void navigate({
                                  to: '/applications/$applicationId/environment',
                                  params: { applicationId: app.id },
                                  search: { service: name },
                                })
                              }
                            >
                              <Icon name="code" size={14} />
                              Environment variables
                            </MenuItem>
                          )}
                          {runtime?.url && /^https?:\/\//.test(runtime.url) && (
                            <MenuItem
                              onSelect={() =>
                                window.open(runtime.url, '_blank', 'noopener,noreferrer')
                              }
                            >
                              <Icon name="external" size={14} />
                              Open endpoint
                            </MenuItem>
                          )}
                          {canDeploy && (
                            <MenuItem onSelect={() => setMovingService(name)}>
                              <Icon name="arrow" size={14} />
                              Move to application
                            </MenuItem>
                          )}
                          {canDeploy && (!service.job || service.job.schedule) && (
                            <MenuItem onSelect={() => setPowerService(name)}>
                              <Icon name={service.suspended ? 'play' : 'pause'} size={14} />
                              {service.job?.schedule
                                ? service.suspended
                                  ? 'Resume schedule'
                                  : 'Pause schedule'
                                : service.suspended
                                  ? 'Resume service'
                                  : 'Stop service'}
                            </MenuItem>
                          )}
                          {canDeploy && (
                            <MenuItem destructive onSelect={() => setDeletingService(name)}>
                              <Icon name="trash" size={14} />
                              Delete service
                            </MenuItem>
                          )}
                        </Menu>
                      }
                      meta={
                        <>
                          {service.public || Object.keys(service.http || {}).length > 0
                            ? 'Public HTTP'
                            : service.port || service.ports?.length
                              ? 'Private service'
                              : service.job
                                ? service.job.schedule
                                  ? 'Scheduled job'
                                  : 'Deployment job'
                                : 'Worker'}{' '}
                          · {serviceProfileLabel(service)}
                          {(service.replicas ?? 1) > 1 && ` ×${service.replicas}`} ·{' '}
                          <span
                            className="app-workspace-image"
                            title={`${runtime?.image ? 'Observed' : 'Requested'} image ${image}`}
                          >
                            {shown.name}
                            {shown.pinned && <small> · pinned</small>}
                          </span>
                        </>
                      }
                      endpoint={
                        runtime?.url && /^https?:\/\//.test(runtime.url) ? (
                          <ExternalEndpoint url={runtime.url} label={label} />
                        ) : address ? (
                          <>
                            <code title={address}>{address}</code>
                            <Copy
                              value={address}
                              label={`Copy ${label} internal address`}
                              iconOnly
                            />
                          </>
                        ) : undefined
                      }
                      state={
                        <ObservedState
                          status={serviceHealth.status}
                          observedAt={serviceHealth.observedAt}
                          note={
                            serviceHealth.status === 'ready' || serviceHealth.status === 'completed'
                              ? undefined
                              : `${replicas}${serviceHealth.note ? ` · ${serviceHealth.note}` : ''}`
                          }
                        />
                      }
                      metric={
                        restarts > 0 ? (
                          <CardMetric label="Restarts" window="since start" value={restarts} />
                        ) : metrics?.available ? (
                          <CardMetric
                            label="CPU"
                            window={`${Math.round(metrics.window_seconds || 0)}s`}
                            value={Math.round(metrics.cpu_millicores || 0)}
                            unit="mCPU"
                          />
                        ) : (
                          <CardMetric
                            label={service.job ? 'Job result' : 'Replicas'}
                            window="now"
                            value={
                              replicas === 'Unavailable' || replicas === 'Not observed'
                                ? undefined
                                : replicas
                            }
                          />
                        )
                      }
                    />
                  )
                })}
              </div>
            )}
            {serviceNames.length > 0 && !observed.length && (
              <Note>
                No runtime observation has been recorded yet. Desired configuration does not mean
                the services are running.
              </Note>
            )}
          </section>
          <section className="app-workspace-section" aria-labelledby="app-activity-heading">
            <div className="app-workspace-section-head">
              <h2 id="app-activity-heading">Recent activity</h2>
              {(app.deployments || []).length > 5 && (
                <Button
                  variant="ghost"
                  size="sm"
                  className="app-workspace-link"
                  onClick={() => setView((current) => ({ ...current, tab: 'activity' }))}
                >
                  View all
                </Button>
              )}
            </div>
            <ActivityList app={app} limit={5} />
          </section>
        </Tabs.Content>
        <Tabs.Content value="activity" className="tab-content app-workspace-overview">
          <section className="app-workspace-section" aria-labelledby="app-activity-all-heading">
            <div className="app-workspace-section-head">
              <h2 id="app-activity-all-heading">Deployments</h2>
              <Button asChild size="sm">
                <Link to="/applications/$applicationId/notifications" params={{ applicationId }}>
                  Notification settings
                </Link>
              </Button>
            </div>
            <ActivityList app={app} />
            <Note>
              Rollback creates a new deployment from recorded image digests and configuration.
              Database migrations and external secret values are not rolled back.
            </Note>
          </section>
        </Tabs.Content>
        <Tabs.Content value="settings" className="tab-content app-settings">
          <nav className="app-settings-nav" aria-label="Application settings">
            {sections.map(([value, label]) => (
              <button
                type="button"
                key={value}
                className="app-settings-link interactive"
                aria-current={view.section === value ? 'page' : undefined}
                onClick={() => openSettings(value)}
              >
                {label}
              </button>
            ))}
            <Link
              className="app-settings-link interactive"
              to="/applications/$applicationId/notifications"
              params={{ applicationId }}
            >
              Deployment notifications
            </Link>
            <Link
              className="app-settings-link interactive"
              to="/alarms/settings"
              search={{
                project: app.project,
                environment: app.environment,
                application_id: app.id,
              }}
            >
              Alarm settings
            </Link>
          </nav>
          <div className="app-settings-panel">
            {view.section === 'details' && (
              <>
                <div className="section-toolbar">
                  <h2>Details</h2>
                </div>
                <dl className="app-workspace-details">
                  <div>
                    <dt>Application ID</dt>
                    <dd>
                      <span className="copy-id">
                        <code>{app.id}</code>
                        <Copy value={app.id} label="Copy application ID" iconOnly />
                      </span>
                    </dd>
                  </div>
                  <div>
                    <dt>Project</dt>
                    <dd>
                      <Link to="/projects/$project" params={{ project: app.project }}>
                        {app.project}
                      </Link>
                    </dd>
                  </div>
                  <div>
                    <dt>Environment</dt>
                    <dd>{app.environment}</dd>
                  </div>
                  <div>
                    <dt>Current revision</dt>
                    <dd>{app.revision}</dd>
                  </div>
                  <div>
                    <dt>Runtime health</dt>
                    <dd>
                      <Status value={health.status} small />
                    </dd>
                  </div>
                  <div>
                    <dt>Networks</dt>
                    <dd>{networks.join(', ')}</dd>
                  </div>
                  <div>
                    <dt>Public endpoints</dt>
                    <dd>{endpoints.length || 'None'}</dd>
                  </div>
                  <div>
                    <dt>Created</dt>
                    <dd>
                      <time title={app.created_at} dateTime={app.created_at}>
                        {relative(app.created_at)}
                      </time>
                    </dd>
                  </div>
                  <div>
                    <dt>Alarms</dt>
                    <dd>
                      <Link
                        to="/alarms"
                        search={{
                          project: app.project,
                          environment: app.environment,
                          application_id: app.id,
                        }}
                      >
                        View alarms
                      </Link>
                    </dd>
                  </div>
                </dl>
              </>
            )}
            {view.section === 'configuration' && (
              <>
                <div className="section-toolbar">
                  <div>
                    <h2>Applied configuration</h2>
                    <p>
                      Revision {app.revision} · Schema version {app.spec.schema_version}
                    </p>
                  </div>
                  <div className="toolbar-actions">
                    <Button size="sm" onClick={() => downloadConfig(app.spec)}>
                      Export TOML
                    </Button>
                    {canDeploy && (
                      <Button
                        size="sm"
                        onClick={() =>
                          void navigate({
                            to: '/applications/$applicationId/configure',
                            params: { applicationId },
                            search: { mode: 'toml' },
                          })
                        }
                      >
                        Edit configuration
                      </Button>
                    )}
                  </div>
                </div>
                <div className="code-panel">
                  <div>
                    <span>
                      <Icon name="code" size={14} />
                      hakopod.toml
                    </span>
                    <Copy value={configuration} label="Copy" />
                  </div>
                  <TOMLCode code={configuration} />
                </div>
                <Note>
                  Saved secret values stay separate from this configuration. Staged edits are
                  validated and reviewed before deployment.
                </Note>
                <div className="section-toolbar app-settings-danger">
                  <div>
                    <h2>Delete application</h2>
                    <p>
                      Remove every service and deploy the empty revision before deleting this
                      application.
                    </p>
                  </div>
                  <DeleteResource project={app.project} application={app} />
                </div>
              </>
            )}
            {view.section === 'source' && features.git && (
              <Suspense fallback={<Loading />}>
                <ApplicationSource application={app} />
              </Suspense>
            )}
            {view.section === 'secrets' && (
              <Suspense fallback={<Loading />}>
                <ApplicationSecrets
                  project={app.project}
                  environment={app.environment}
                  application={app.name}
                />
              </Suspense>
            )}
            {view.section === 'previews' && <ApplicationPreviews application={app} />}
            {view.section === 'networking' && (
              <>
                <div className="section-toolbar">
                  <div className="hako-section-heading-title">
                    <h2>Networking</h2>
                    <HeadingHelp title="Networking">
                      Service discovery, private networks, and public domains.
                    </HeadingHelp>
                  </div>
                  <Button asChild>
                    <Link to="/applications/$applicationId/domains" params={{ applicationId }}>
                      Manage custom domains
                    </Link>
                  </Button>
                </div>
                <ResourceRows label="Service network exposure">
                  {Object.entries(app.spec.services).map(([name, service]) => {
                    const address =
                      observed.find((s) => s.name === name)?.internal_address ||
                      (service.port ? `${name}:${service.port}` : '')
                    return (
                      <ResourceRow
                        key={name}
                        identity={<strong>{app.service_display_names?.[name] || name}</strong>}
                        detail={
                          address ? (
                            <span className="copy-id">
                              <code>{address}</code>
                              <Copy value={address} label={`Copy ${name} address`} iconOnly />
                            </span>
                          ) : (
                            <span className="muted-text">No inbound port</span>
                          )
                        }
                        status={
                          <span className="label-chip">
                            {service.public ? 'Public HTTP' : 'Private'}
                          </span>
                        }
                        meta={(service.networks?.length ? service.networks : ['default']).join(
                          ', ',
                        )}
                      />
                    )
                  })}
                </ResourceRows>
                <Note>
                  Internal addresses work between permitted services in this application. Browser
                  JavaScript cannot reach them directly; proxy requests through your public web
                  server.
                </Note>
                {Object.values(app.spec.networks || {}).some((network) => network.internal) && (
                  <Note>
                    Internal-only network membership removes general internet egress. A service
                    attached to another network with egress can still reach the internet through
                    that network.
                  </Note>
                )}
                <Suspense fallback={<Loading rows={2} />}>
                  <IssuerSettings key={app.id} application={app} />
                </Suspense>
              </>
            )}
          </div>
        </Tabs.Content>
      </Tabs.Root>
    </div>
  )
}
