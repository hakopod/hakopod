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
import { readinessLabel } from '../lib/readiness'
import { downloadConfig, specToTOML } from '../lib/toml'
import { useScope } from '../lib/scope'
import { useActiveSection } from '../lib/use-active-section'
import {
  applicationRuntimeHealth,
  runtimeReplicaSummary,
  serviceRuntimeHealth,
} from '../lib/runtime-health'
import { ApplicationAlarmLinks, RuntimeNotice } from '../components/runtime-notice'
import { Icon } from '../components/icons'
import { Button } from '../components/ui/button'
import { Brackets } from '@hakopod/hatch-ui/components/brackets'
import { ServiceImageIcon } from '../components/service-image-icon'
import { Menu, MenuItem } from '@hakopod/hatch-ui/components/dropdown-menu'
import { HeadingHelp, Copy, Empty, ErrorState, Loading, Note, Status } from '../components/shared'
import { Logs } from '../components/logs'
import { TOMLCode } from '../components/toml-code'
const ServiceDetail = lazy(() =>
  import('../components/service-detail').then((m) => ({ default: m.ServiceDetail })),
)
const SampleBanner = lazy(() => import('../components/sample-banner'))
const PodTerminal = lazy(() => import('../components/pod-terminal'))
const ApplicationTopology = lazy(() => import('../components/application-topology'))
const ApplicationSecrets = lazy(() => import('../components/application-secrets'))
const ApplicationSource = lazy(() => import('../components/application-source'))

export const Route = createFileRoute('/applications/$applicationId')({
  validateSearch: (
    search: Record<string, unknown>,
  ): { service?: string; tab?: string; pod?: string } => ({
    service: typeof search.service === 'string' ? search.service : undefined,
    tab:
      typeof search.tab === 'string' &&
      [
        'topology',
        'services',
        'deployments',
        'logs',
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
function ApplicationDetail() {
  const { applicationId } = Route.useParams()
  const { service: selectedService, tab: selectedTab, pod: selectedPod } = Route.useSearch()
  const features = useEditionFeatures()
  const scope = useScope()
  const navigate = useNavigate()
  const [tab, setTab] = useState(selectedTab || 'services')
  const navigationRoot = useActiveSection(tab, '.tab-list')
  useEffect(() => {
    if (selectedTab) setTab(selectedTab)
  }, [selectedTab])
  const [powerService, setPowerService] = useState('')
  const [deletingService, setDeletingService] = useState('')
  const [logService, setLogService] = useState('')
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
  if (application.isPending) return <Loading rows={4} />
  if (application.error || !application.data)
    return <ErrorState error={application.error} retry={() => void application.refetch()} />
  const app = application.data
  const health = applicationRuntimeHealth(app)
  const serviceNames = Object.keys(app.spec.services)
  const observed = app.observed?.services || []
  const endpoint = observed.find((service) => service.url)?.url
  if (selectedService)
    return (
      <Suspense fallback={<Loading />}>
        <ServiceDetail
          key={`${app.id}:${selectedService}`}
          application={app}
          serviceName={selectedService}
          initialTab={selectedTab}
          initialPod={selectedPod}
        />
      </Suspense>
    )
  return (
    <div className="ops-page">
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
      <Suspense fallback={null}>
        <SampleBanner applicationId={app.id} />
      </Suspense>
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
      <div className="application-heading">
        <div>
          <div className="title-row hako-page-heading-title">
            <Status value={health.status} />
            <h1>{app.name}</h1>
          </div>
          <div className="application-metadata">
            <code>{app.id}</code>
            <Copy value={app.id} />
            <span>Revision {app.revision}</span>
            <span>
              {serviceNames.length} {serviceNames.length === 1 ? 'service' : 'services'}
            </span>
            <span>Updated {relative(app.updated_at)}</span>
            {endpoint && /^https?:\/\//.test(endpoint) && (
              <a
                className="application-endpoint-link"
                href={endpoint}
                target="_blank"
                rel="noreferrer"
                aria-label={`${endpoint} (opens in a new tab)`}
                title={endpoint}
              >
                <span className="application-endpoint-text">
                  {endpoint.replace(/^https?:\/\//, '')}
                </span>
                <Icon name="external" size={12} />
              </a>
            )}
          </div>
        </div>
        <div className="form-spacer" />
        {scope.can('deployments:write') && (
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
            <Icon name="plus" size={16} />
            Deploy changes
          </Button>
        )}
      </div>
      <RuntimeNotice
        health={health}
        applicationId={app.id}
        canInspectNodes={scope.can('admin')}
        canInspectLogs={scope.can('logs:read')}
      />
      <Tabs.Root
        value={
          (tab === 'terminal' && !features.terminal) || (tab === 'source' && !features.git)
            ? 'services'
            : tab
        }
        onValueChange={setTab}
      >
        <Tabs.List
          ref={navigationRoot}
          className="tab-list application-tabs"
          aria-label="Application sections"
        >
          {[
            ['services', 'box', 'Services'],
            ['topology', 'network', 'Topology'],
            ['deployments', 'branch', 'Deployments'],
            ['logs', 'activity', 'Logs'],
            ['terminal', 'terminal', 'Terminal'],
            ['networking', 'network', 'Networking'],
            ['configuration', 'settings', 'Configuration'],
            ['source', 'branch', 'Source'],
            ['secrets', 'lock', 'Secrets'],
          ]
            .filter(
              ([value]) =>
                (value !== 'terminal' || features.terminal) && (value !== 'source' || features.git),
            )
            .map(([value, icon, label]) => (
              <Tabs.Trigger className="tab-trigger" key={value} value={value}>
                <Icon name={icon} size={15} />
                {label}
              </Tabs.Trigger>
            ))}
        </Tabs.List>
        <Tabs.Content value="topology" className="tab-content">
          <Suspense fallback={<Loading />}>
            <ApplicationTopology application={app} />
          </Suspense>
        </Tabs.Content>
        <Tabs.Content value="terminal" className="tab-content">
          <Suspense fallback={<Loading />}>
            <PodTerminal applicationId={app.id} services={serviceNames} initialPod={selectedPod} />
          </Suspense>
        </Tabs.Content>
        <Tabs.Content value="services" className="tab-content">
          <div className="ops-section-description">
            <p>Open a service for pods, logs, resource usage, and configuration.</p>
            <span className="label-chip">
              <Icon name="network" size={12} />
              Application private network
            </span>
          </div>
          <div className="ops-catalog-grid ops-service-catalog" aria-label="Service cards">
            {Object.entries(app.spec.services).map(([name, service]) => {
              const runtime = observed.find((status) => status.name === name)
              const serviceHealth = serviceRuntimeHealth(runtime, app.observed?.observed_at)
              return (
                <article key={name} className="ops-catalog-card interactive">
                  <Brackets />
                  <div className="ops-card-heading">
                    <ServiceImageIcon image={runtime?.image || service.image} />
                    <Status value={serviceHealth.status} small />
                    <div className="ops-card-actions">
                      <Menu
                        trigger={
                          <Button variant="ghost" size="icon" aria-label={`Actions for ${name}`}>
                            <span aria-hidden="true">···</span>
                          </Button>
                        }
                      >
                        <MenuItem
                          onSelect={() =>
                            void navigate({
                              to: '/applications/$applicationId',
                              params: { applicationId: app.id },
                              search: { service: name },
                            })
                          }
                        >
                          <Icon name="box" size={14} />
                          Inspect service
                        </MenuItem>
                        {scope.can('logs:read') && (
                          <MenuItem
                            onSelect={() => {
                              setLogService(name)
                              setTab('logs')
                            }}
                          >
                            <Icon name="terminal" size={14} />
                            View logs
                          </MenuItem>
                        )}
                        {scope.can('deployments:write') && (
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
                        {scope.can('deployments:write') && !service.job && (
                          <MenuItem onSelect={() => setPowerService(name)}>
                            <Icon name={service.suspended ? 'play' : 'pause'} size={14} />
                            {service.suspended ? 'Resume service' : 'Stop service'}
                          </MenuItem>
                        )}
                        {scope.can('deployments:write') && (
                          <MenuItem destructive onSelect={() => setDeletingService(name)}>
                            <Icon name="trash" size={14} />
                            Delete service
                          </MenuItem>
                        )}
                      </Menu>
                    </div>
                  </div>
                  <h2>
                    <Link
                      className="ops-card-link"
                      to="/applications/$applicationId"
                      params={{ applicationId: app.id }}
                      search={{ service: name }}
                      aria-label={`Open ${name} service`}
                    >
                      {name}
                    </Link>
                  </h2>
                  <div className="ops-object-id">
                    <code title={runtime?.image || service.image}>
                      {runtime?.image || service.image}
                    </code>
                    <Copy value={runtime?.image || service.image} />
                  </div>
                  <small className="ops-card-caption">
                    {runtime?.image ? 'Observed image' : 'Requested image'}
                  </small>
                  <dl className="ops-card-facts">
                    <div>
                      <dt>{service.job ? 'Job result' : 'Replicas'}</dt>
                      <dd>{runtimeReplicaSummary(serviceHealth, Boolean(service.job))}</dd>
                    </div>
                    <div>
                      <dt>Exposure</dt>
                      <dd>
                        {service.public || Object.keys(service.http || {}).length > 0
                          ? 'Public HTTP'
                          : service.port || service.ports?.length
                            ? 'Private'
                            : service.job
                              ? 'Deployment job'
                              : 'Worker'}
                      </dd>
                    </div>
                    <div>
                      <dt>Profile</dt>
                      <dd>{service.size || 'small'}</dd>
                    </div>
                    <div>
                      <dt>Readiness</dt>
                      <dd>{readinessLabel(service)}</dd>
                    </div>
                  </dl>
                  {runtime?.message && !serviceHealth.note && (
                    <p className="ops-card-message">{runtime.message}</p>
                  )}
                  <div className="ops-card-footer">
                    <span>Inspect service</span>
                    <Icon name="arrow" size={15} />
                  </div>
                </article>
              )
            })}
          </div>
          {!observed.length && (
            <Note>
              No runtime observation has been recorded yet. Desired configuration above does not
              mean the services are running.
            </Note>
          )}
          <Link className="resource-note interactive" to="/infrastructure">
            <Icon name="activity" size={18} />
            <div>
              <strong>Resource metrics</strong>
              <p>
                Open a service to inspect its live CPU, memory, pods, and events. Node capacity is
                available in Infrastructure.
              </p>
            </div>
            <Icon name="arrow" size={18} />
          </Link>
          <ApplicationAlarmLinks application={app} />
        </Tabs.Content>
        <Tabs.Content value="deployments" className="tab-content">
          <DeploymentHistory application={app} />
        </Tabs.Content>
        <Tabs.Content value="logs" className="tab-content">
          {scope.can('logs:read') ? (
            <Logs applicationId={app.id} services={serviceNames} initialService={logService} />
          ) : (
            <Empty
              icon="lock"
              title="Logs need additional permission"
              description="Your account needs logs:read for this application."
            />
          )}
        </Tabs.Content>
        <Tabs.Content value="networking" className="tab-content">
          <div className="section-toolbar">
            <div>
              <div className="hako-section-heading-title">
                <h2>Networking</h2>
                <HeadingHelp title="Networking">
                  Service discovery, private networks, and public domains.
                </HeadingHelp>
              </div>
            </div>
            <Button asChild>
              <Link to="/applications/$applicationId/domains" params={{ applicationId }}>
                Manage custom domains
                <Icon name="globe" size={14} />
              </Link>
            </Button>
          </div>
          <div className="table-container">
            <table>
              <thead>
                <tr>
                  <th>Service</th>
                  <th>Exposure</th>
                  <th>Internal address</th>
                  <th>Networks</th>
                </tr>
              </thead>
              <tbody>
                {Object.entries(app.spec.services).map(([name, service]) => (
                  <tr key={name}>
                    <td>
                      <span className="table-name">
                        <Icon name="box" size={15} />
                        {name}
                      </span>
                    </td>
                    <td>
                      <span className={`exposure-label ${service.public ? 'public-label' : ''}`}>
                        <Icon name={service.public ? 'globe' : 'lock'} size={12} />
                        {service.public ? 'Public HTTP' : 'Private'}
                      </span>
                    </td>
                    <td>
                      {service.port || service.ports?.length ? (
                        <div className="service-address-list">
                          {Boolean(service.port) && (
                            <span className="copyable-address">
                              <code>
                                {observed.find((s) => s.name === name)?.internal_address ||
                                  `${name}:${service.port}`}
                              </code>
                              <Copy
                                value={
                                  observed.find((s) => s.name === name)?.internal_address ||
                                  `${name}:${service.port}`
                                }
                              />
                            </span>
                          )}
                          {service.ports?.map((port) => (
                            <span className="copyable-address" key={port.name}>
                              <code>
                                {name}:{port.port}
                              </code>
                              <small>{port.protocol}</small>
                              <Copy value={`${name}:${port.port}`} />
                            </span>
                          ))}
                        </div>
                      ) : (
                        <span className="muted-text">No inbound port</span>
                      )}
                    </td>
                    <td>
                      <div className="service-pills">
                        {(service.networks?.length ? service.networks : ['default']).map(
                          (network) => (
                            <span key={network}>
                              {network}
                              {app.spec.networks?.[network]?.internal && (
                                <Icon name="lock" size={11} />
                              )}
                            </span>
                          ),
                        )}
                      </div>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
          <Note>
            Internal addresses work between permitted services in this application. Browser
            JavaScript cannot reach them directly; proxy requests through your public web server.
          </Note>
          {Object.values(app.spec.networks || {}).some((network) => network.internal) && (
            <Note>
              Internal-only network membership removes general internet egress. A service attached
              to another network with egress can still reach the internet through that network.
            </Note>
          )}
        </Tabs.Content>
        <Tabs.Content value="configuration" className="tab-content">
          <div className="section-toolbar">
            <span className="field-help">
              Remove every service and deploy the empty revision before deleting this application.
            </span>
            <DeleteResource project={application.data.project} application={application.data} />
          </div>
          <div className="section-toolbar">
            <div>
              <h2>Applied configuration</h2>
              <p>
                Revision {app.revision} · Schema version {app.spec.schema_version}
              </p>
            </div>
            <div className="toolbar-actions">
              <Button size="sm" onClick={() => downloadConfig(app.spec)}>
                <Icon name="code" size={14} />
                Export TOML
              </Button>
              {scope.can('deployments:write') && (
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
                  <Icon name="settings" size={14} />
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
            Saved secret values stay separate from this configuration. Staged edits are validated
            and reviewed before deployment.
          </Note>
        </Tabs.Content>
        <Tabs.Content value="source" className="tab-content">
          <Suspense fallback={<Loading />}>
            <ApplicationSource application={app} />
          </Suspense>
        </Tabs.Content>
        <Tabs.Content value="secrets" className="tab-content">
          <Suspense fallback={<Loading />}>
            <ApplicationSecrets
              project={app.project}
              environment={app.environment}
              application={app.name}
            />
          </Suspense>
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
      </div>
      {error && (
        <div className="inline-error" role="alert">
          {error}
        </div>
      )}
      {!deployments.length ? (
        <Empty
          icon="branch"
          title="No deployments recorded"
          description="Accepted releases appear here with their status and service results."
        />
      ) : (
        <div className="table-container ops-table">
          <table>
            <thead>
              <tr>
                <th>Revision / deployment outcome</th>
                <th>Deployment</th>
                <th>Created</th>
                <th>
                  <span className="sr-only">Actions</span>
                </th>
              </tr>
            </thead>
            <tbody>
              {[...deployments]
                .sort((a, b) => b.revision - a.revision)
                .map((deployment) => (
                  <tr key={deployment.id} className="ops-linked-row">
                    <td>
                      <div className="ops-object">
                        <Status value={deployment.status} small />
                        <Link
                          className="ops-object-name ops-row-link"
                          to="/deployments/$deploymentId"
                          params={{ deploymentId: deployment.id }}
                          aria-label={`Inspect deployment revision ${deployment.revision}`}
                        >
                          r{deployment.revision}
                        </Link>
                        {deployment.revision === application.revision && (
                          <span className="current-label">Current revision</span>
                        )}
                      </div>
                      {deployment.revision === application.revision && (
                        <div className="ops-table-sub runtime-summary">
                          <span>Runtime now</span>
                          <Status value={health.status} small />
                        </div>
                      )}
                    </td>
                    <td>
                      <div className="ops-object-id">
                        <code>{deployment.id}</code>
                        <Copy value={deployment.id} />
                      </div>
                    </td>
                    <td>
                      <time title={deployment.created_at} dateTime={deployment.created_at}>
                        {relative(deployment.created_at)}
                      </time>
                    </td>
                    <td>
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
                    </td>
                  </tr>
                ))}
            </tbody>
          </table>
        </div>
      )}
      <Note>
        Rollback creates a new deployment from recorded image digests and configuration. Database
        migrations and external secret values are not rolled back.
      </Note>
    </>
  )
}
