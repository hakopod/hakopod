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
import { useQuery } from '@tanstack/react-query'
import * as Tabs from '@radix-ui/react-tabs'
import type { Application, DeploymentSummary } from '../lib/types'
import { APIError, message, relative } from '../lib/api'
import { client, unwrap } from '../lib/client'
import { downloadConfig, specToTOML } from '../lib/toml'
import { useScope } from '../lib/scope'
import { useActiveSection } from '../lib/use-active-section'
import {
  applicationRuntimeHealth,
  runtimeReplicaSummary,
  serviceRuntimeHealth,
} from '../lib/runtime-health'
import { RuntimeNotice } from '../components/runtime-notice'
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
import {
  HeadingHelp,
  Copy,
  ErrorState,
  Loading,
  Note,
  Status,
  RequestError,
} from '../components/shared'
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
      ].includes(search.tab)
        ? search.tab
        : undefined,
    run: typeof search.run === 'string' ? search.run.slice(0, 256) : undefined,
    job: typeof search.job === 'string' ? search.job.slice(0, 64) : undefined,
    runner: typeof search.runner === 'string' ? search.runner.slice(0, 64) : undefined,
    pod: typeof search.pod === 'string' ? search.pod : undefined,
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
const applicationTabs = ['overview', 'deployments', 'topology', 'previews', 'settings'] as const
const settingsSections = ['configuration', 'source', 'secrets', 'networking'] as const
type SettingsSection = (typeof settingsSections)[number]

// Older links used one tab per task; map them onto the overview, settings or a service.
function initialView(tab?: string): { tab: string; section: SettingsSection } {
  if (tab && (settingsSections as readonly string[]).includes(tab))
    return { tab: 'settings', section: tab as SettingsSection }
  if (tab && (applicationTabs as readonly string[]).includes(tab))
    return { tab, section: 'configuration' }
  return { tab: 'overview', section: 'configuration' }
}

// "docker.io/library/python@sha256:…" reads as "python" with a pinned marker; the
// full reference stays in the title and the service inspector.
function imageLabel(image: string) {
  const [reference, digest] = image.split('@')
  const name = reference.split('/').pop() || reference
  return { name, pinned: Boolean(digest) }
}

function ApplicationDetail() {
  const { applicationId } = Route.useParams()
  const { service: selectedService, tab: selectedTab, pod: selectedPod } = Route.useSearch()
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
  const configuration = useMemo(
    () => (application.data ? specToTOML(application.data.spec) : ''),
    [application.data?.spec],
  )
  useEffect(() => {
    if (application.data) scope.syncScope(application.data.project, application.data.environment)
  }, [application.data?.project, application.data?.environment, scope.syncScope])
  // Logs and the terminal belong to a service: send older application-level links there.
  const firstService = application.data ? Object.keys(application.data.spec.services)[0] : ''
  useEffect(() => {
    if (!selectedService && firstService && (selectedTab === 'logs' || selectedTab === 'terminal'))
      void navigate({
        to: '/applications/$applicationId',
        params: { applicationId },
        search: { service: firstService, tab: selectedTab, pod: selectedPod },
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
  const openService = (name: string, tab?: string) =>
    void navigate({
      to: '/applications/$applicationId',
      params: { applicationId: app.id },
      search: { service: name, ...(tab ? { tab } : {}) },
    })
  const openSettings = (section: SettingsSection) => setView({ tab: 'settings', section })
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
  const recent = [...(app.deployments || [])].sort((a, b) => b.revision - a.revision)
  const latest = recent[0]
  const networks = [
    ...new Set(Object.values(app.spec.services).flatMap((s) => s.networks || ['default'])),
  ]
  const sections: [SettingsSection, string][] = [
    ['configuration', 'Configuration'],
    ...(features.git ? ([['source', 'Source']] as [SettingsSection, string][]) : []),
    ['secrets', 'Secrets'],
    ['networking', 'Networking and TLS'],
  ]
  return (
    <div className="ops-page app-workspace">
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
            <ObservedState
              status={health.status}
              observedAt={app.observed?.observed_at}
              note={health.note}
            />
            <span>Revision {app.revision}</span>
            <span>
              {serviceNames.length} {serviceNames.length === 1 ? 'service' : 'services'}
            </span>
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
            <MenuItem onSelect={() => openSettings('configuration')}>
              <Icon name="settings" size={14} />
              Application settings
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
            ['deployments', 'Deployments'],
            ['topology', 'Topology'],
            ['previews', 'Previews'],
            ['settings', 'Settings'],
          ].map(([value, label]) => (
            <Tabs.Trigger className="tab-trigger" key={value} value={value}>
              {label}
            </Tabs.Trigger>
          ))}
        </Tabs.List>
        <Tabs.Content value="overview" className="tab-content app-workspace-overview">
          <RuntimeNotice
            health={health}
            applicationId={app.id}
            canInspectNodes={scope.can('admin')}
            canInspectLogs={scope.can('logs:read')}
          />
          <ApplicationSecurity applicationId={app.id} attentionOnly />
          <VolumeResizeOperations application={app} />
          {canDeploy && <ServiceMoves application={app} />}
          <ManagedActionsStatus application={app} />
          <section className="app-workspace-section" aria-labelledby="app-services-heading">
            <div className="app-workspace-section-head">
              <h2 id="app-services-heading">Services</h2>
              <span className="app-workspace-count">{serviceNames.length}</span>
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
            ) : (
              <div className="resource-grid" aria-label="Service cards">
                {Object.entries(app.spec.services).map(([name, service]) => {
                  const runtime = observed.find((status) => status.name === name)
                  const serviceHealth = serviceRuntimeHealth(runtime, app.observed?.observed_at)
                  const label = app.service_display_names?.[name] || name
                  const image = runtime?.image || service.image
                  const shown = imageLabel(image)
                  const replicas = runtimeReplicaSummary(serviceHealth, Boolean(service.job))
                  const address =
                    runtime?.internal_address || (service.port ? `${name}:${service.port}` : '')
                  return (
                    <ResourceCard
                      key={name}
                      cue={<ServiceImageIcon image={image} actions={service.actions} />}
                      name={
                        <Link
                          className="ops-card-link"
                          to="/applications/$applicationId"
                          params={{ applicationId: app.id }}
                          search={{ service: name }}
                          aria-label={`Open ${label} service`}
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
                          <MenuItem onSelect={() => openService(name)}>
                            <Icon name="box" size={14} />
                            Inspect service
                          </MenuItem>
                          {scope.can('logs:read') && (
                            <MenuItem onSelect={() => openService(name, 'logs')}>
                              <Icon name="activity" size={14} />
                              View logs
                            </MenuItem>
                          )}
                          {features.terminal && (
                            <MenuItem onSelect={() => openService(name, 'terminal')}>
                              <Icon name="terminal" size={14} />
                              Open terminal
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
                          · {serviceProfileLabel(service)} ·{' '}
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
                          note={serviceHealth.note || runtime?.message}
                        />
                      }
                      metric={
                        <CardMetric
                          label={service.job ? 'Job result' : 'Replicas'}
                          window="now"
                          value={
                            replicas === 'Unavailable' || replicas === 'Not observed'
                              ? undefined
                              : replicas
                          }
                        />
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
          <div className="app-workspace-columns">
            <section className="app-workspace-section" aria-labelledby="app-deployments-heading">
              <div className="app-workspace-section-head">
                <h2 id="app-deployments-heading">Deployments</h2>
                {recent.length > 0 && (
                  <Button
                    variant="ghost"
                    size="sm"
                    className="app-workspace-link"
                    onClick={() => setView((current) => ({ ...current, tab: 'deployments' }))}
                  >
                    View all
                  </Button>
                )}
              </div>
              {!recent.length ? (
                <p className="app-workspace-empty">No deployments recorded yet.</p>
              ) : (
                <ResourceRows label="Recent deployments, most recent first">
                  {recent.slice(0, 5).map((deployment) => (
                    <ResourceRow
                      key={deployment.id}
                      identity={
                        <Link
                          className="resource-row-link"
                          to="/deployments/$deploymentId"
                          params={{ deploymentId: deployment.id }}
                          aria-label={`Inspect deployment revision ${deployment.revision}`}
                        >
                          Revision {deployment.revision}
                          {deployment.revision === app.revision && (
                            <span className="current-label">Current</span>
                          )}
                        </Link>
                      }
                      status={<Status value={deployment.status} small />}
                      meta={
                        <time title={deployment.created_at} dateTime={deployment.created_at}>
                          {relative(deployment.created_at)}
                        </time>
                      }
                    />
                  ))}
                </ResourceRows>
              )}
            </section>
            <section className="app-workspace-section" aria-labelledby="app-details-heading">
              <div className="app-workspace-section-head">
                <h2 id="app-details-heading">Environment details</h2>
              </div>
              <dl className="app-workspace-details">
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
                  <dd>
                    {app.revision}
                    {latest && (
                      <>
                        {' · '}
                        <Status value={latest.status} small />
                      </>
                    )}
                  </dd>
                </div>
                <div>
                  <dt>Networks</dt>
                  <dd>
                    <Button
                      variant="ghost"
                      size="sm"
                      className="app-workspace-link"
                      onClick={() => openSettings('networking')}
                    >
                      {networks.join(', ')}
                    </Button>
                  </dd>
                </div>
                <div>
                  <dt>Public endpoints</dt>
                  <dd>
                    {endpoints.length ? (
                      <Button
                        variant="ghost"
                        size="sm"
                        className="app-workspace-link"
                        onClick={() => openSettings('networking')}
                      >
                        {endpoints.length} {endpoints.length === 1 ? 'endpoint' : 'endpoints'}
                      </Button>
                    ) : (
                      'None'
                    )}
                  </dd>
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
            </section>
          </div>
        </Tabs.Content>
        <Tabs.Content value="deployments" className="tab-content">
          <DeploymentHistory application={app} />
        </Tabs.Content>
        <Tabs.Content value="topology" className="tab-content">
          <Suspense fallback={<Loading />}>
            <ApplicationTopology application={app} />
          </Suspense>
        </Tabs.Content>
        <Tabs.Content value="previews" className="tab-content">
          <ApplicationPreviews application={app} />
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

function DeploymentHistory({ application }: { application: Application }) {
  const scope = useScope()
  const navigate = useNavigate()
  const deployments = application.deployments || []
  const health = applicationRuntimeHealth(application)
  const [busy, setBusy] = useState('')
  const [error, setError] = useState('')
  const rollbackRequest = useRef<{
    applicationId: string
    revision: number
    expectedRevision: number
    key: string
    pending: boolean
  } | null>(null)
  async function rollback(deployment: DeploymentSummary) {
    if (rollbackRequest.current?.pending) return
    // Retain the same request after a lost response, even if the app refreshes.
    if (
      rollbackRequest.current?.applicationId !== application.id ||
      rollbackRequest.current.revision !== deployment.revision
    ) {
      rollbackRequest.current = {
        applicationId: application.id,
        revision: deployment.revision,
        expectedRevision: application.revision,
        key: crypto.randomUUID(),
        pending: false,
      }
    }
    const request = rollbackRequest.current
    request.pending = true
    setBusy(deployment.id)
    setError('')
    try {
      const accepted = await unwrap(
        client.POST('/applications/{id}/rollback', {
          params: {
            path: { id: request.applicationId },
            header: { 'Idempotency-Key': request.key },
          },
          body: { revision: request.revision, expected_revision: request.expectedRevision },
        }),
      )
      void navigate({ to: '/deployments/$deploymentId', params: { deploymentId: accepted.id } })
    } catch (err) {
      if (err instanceof APIError && [400, 403, 404, 409, 422].includes(err.status))
        rollbackRequest.current = null
      setError(message(err))
    } finally {
      request.pending = false
      setBusy('')
    }
  }
  return (
    <>
      <div className="section-toolbar">
        <div>
          <div className="hako-section-heading-title">
            <h2>Deployment history</h2>
            <HeadingHelp title="Deployment history">
              Recorded deployment outcomes, most recent first. Runtime health can change after a
              deployment finishes. Open a release for events and its configuration diff.
            </HeadingHelp>
          </div>
        </div>
        <Button asChild>
          <Link
            to="/applications/$applicationId/notifications"
            params={{ applicationId: application.id }}
          >
            Notification settings
          </Link>
        </Button>
      </div>
      {error && <RequestError error={error} />}
      {!deployments.length ? (
        <ResourceEmpty
          icon="branch"
          title="No deployments recorded"
          description="Accepted releases appear here with their outcome and service results."
          action={
            scope.can('deployments:write') && (
              <Button asChild>
                <Link
                  to="/applications/$applicationId/configure"
                  params={{ applicationId: application.id }}
                  search={{ mode: 'form' }}
                >
                  Deploy changes
                </Link>
              </Button>
            )
          }
        />
      ) : (
        <ResourceRows label="Deployments, most recent first">
          {[...deployments]
            .sort((a, b) => b.revision - a.revision)
            .map((deployment) => (
              <ResourceRow
                key={deployment.id}
                identity={
                  <>
                    <Link
                      className="resource-row-link"
                      to="/deployments/$deploymentId"
                      params={{ deploymentId: deployment.id }}
                      aria-label={`Inspect deployment revision ${deployment.revision}`}
                    >
                      r{deployment.revision}
                    </Link>
                    {deployment.revision === application.revision && (
                      <span className="current-label">Current revision</span>
                    )}
                  </>
                }
                detail={
                  <>
                    <span className="copy-id">
                      <code>{deployment.id}</code>
                      <Copy
                        value={deployment.id}
                        label={`Copy deployment ID ${deployment.id}`}
                        iconOnly
                      />
                    </span>
                    {deployment.revision === application.revision && (
                      <span className="runtime-summary">
                        Runtime now <Status value={health.status} small />
                      </span>
                    )}
                  </>
                }
                status={<Status value={deployment.status} small />}
                meta={
                  <time title={deployment.created_at} dateTime={deployment.created_at}>
                    {relative(deployment.created_at)}
                  </time>
                }
                actions={
                  <Menu
                    trigger={
                      <Button
                        variant="ghost"
                        size="icon"
                        aria-label={`Actions for revision ${deployment.revision}`}
                      >
                        <span aria-hidden="true">···</span>
                      </Button>
                    }
                  >
                    <MenuItem
                      onSelect={() =>
                        void navigate({
                          to: '/deployments/$deploymentId',
                          params: { deploymentId: deployment.id },
                        })
                      }
                    >
                      Inspect deployment
                    </MenuItem>
                    {scope.can('deployments:write') &&
                      deployment.status === 'succeeded' &&
                      deployment.revision < application.revision && (
                        <MenuItem
                          disabled={Boolean(busy)}
                          onSelect={() => void rollback(deployment)}
                        >
                          {busy === deployment.id
                            ? 'Creating rollback…'
                            : 'Roll back to this revision'}
                        </MenuItem>
                      )}
                  </Menu>
                }
              />
            ))}
        </ResourceRows>
      )}
      <Note>
        Rollback creates a new deployment from recorded image digests and configuration. Database
        migrations and external secret values are not rolled back. Deploying from CI?{' '}
        <a href="https://hakopod.com/docs/ci-deployments/" target="_blank" rel="noreferrer">
          Read the deployment API guide
        </a>
        .
      </Note>
    </>
  )
}
