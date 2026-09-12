import { lazy, Suspense, useEffect, useMemo, useRef, useState } from 'react'
import { createFileRoute, Link, useNavigate, useLocation, Outlet } from '@tanstack/react-router'
import { useQuery } from '@tanstack/react-query'
import * as Tabs from '@radix-ui/react-tabs'
import type { Application, DeploymentSummary } from '../lib/types'
import { APIError, message, relative, timestamp } from '../lib/api'
import { client, unwrap } from '../lib/client'
import { downloadConfig, specToTOML } from '../lib/toml'
import { useScope } from '../lib/scope'
import { Icon } from '../components/icons'
import { Button } from '../components/ui/button'
import { Menu, MenuItem } from '@hakopod/hatch-ui/components/dropdown-menu'
import { Copy, Empty, ErrorState, Loading, Note, Status } from '../components/shared'
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
  const scope = useScope()
  const navigate = useNavigate()
  const [tab, setTab] = useState(selectedTab || 'services')
  useEffect(() => {
    if (selectedTab) setTab(selectedTab)
  }, [selectedTab])
  const [logService, setLogService] = useState('')
  const application = useQuery({
    queryKey: ['application', applicationId],
    queryFn: ({ signal }) =>
      unwrap(client.GET('/applications/{id}', { signal, params: { path: { id: applicationId } } })),
    refetchInterval: 10000,
    refetchIntervalInBackground: false,
    gcTime: 0,
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
      <Suspense fallback={null}>
        <SampleBanner applicationId={app.id} />
      </Suspense>
      <Link to="/" className="back-link">
        <Icon name="back" size={14} />
        All applications
      </Link>
      <div className="application-heading">
        <div className="app-symbol app-symbol-large">
          <Icon name="box" size={27} />
        </div>
        <div>
          <div className="title-row">
            <Status value={app.observed?.status || 'not observed'} />
            <h1>{app.name}</h1>
          </div>
          <div className="application-metadata">
            <code>{app.id}</code>
            <Copy value={app.id} />
            <span>
              <Icon name="branch" size={13} />
              Revision {app.revision} · {app.status}
            </span>
            <span>
              <Icon name="box" size={13} />
              {serviceNames.length} {serviceNames.length === 1 ? 'service' : 'services'}
            </span>
            <span>Updated {relative(app.updated_at)}</span>
          </div>
        </div>
        <div className="form-spacer" />
        {scope.can('deployments:write') && (
          <Button
            variant="primary"
            className="button-deploy"
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
      <div className="application-context-strip">
        <span>
          <Icon name="box" size={14} />
          {app.project}
          <span className="context-slash">/</span>
          {app.environment}
        </span>
        <span className="context-strip-divider" />
        <span>
          <Icon name={endpoint ? 'globe' : 'lock'} size={14} />
          {endpoint && /^https?:\/\//.test(endpoint) ? (
            <a href={endpoint} target="_blank" rel="noreferrer">
              {endpoint.replace(/^https?:\/\//, '')}
              <Icon name="external" size={12} />
            </a>
          ) : (
            'No public endpoint observed'
          )}
        </span>
        <div className="form-spacer" />
        <span className="observed-time">
          Refreshed {timestamp(new Date(application.dataUpdatedAt).toISOString())}
        </span>
      </div>
      <Tabs.Root value={tab} onValueChange={setTab}>
        <Tabs.List className="tab-list application-tabs" aria-label="Application sections">
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
          ].map(([value, icon, label]) => (
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
          <div className="section-toolbar">
            <div>
              <h2>Services</h2>
              <p>
                Observed state and desired configuration. Open a service for pods, logs, and
                resource usage.
              </p>
            </div>
            <span className="label-chip">
              <Icon name="network" size={12} />
              Application private network
            </span>
          </div>
          <div className="table-container ops-table">
            <table>
              <thead>
                <tr>
                  <th>Service</th>
                  <th>Image</th>
                  <th>Replicas</th>
                  <th>Exposure</th>
                  <th>Profile</th>
                  <th>
                    <span className="sr-only">Service actions</span>
                  </th>
                </tr>
              </thead>
              <tbody>
                {Object.entries(app.spec.services).map(([name, service]) => {
                  const runtime = observed.find((status) => status.name === name)
                  return (
                    <tr key={name}>
                      <td>
                        <div className="ops-object">
                          <Status value={runtime?.status || 'not observed'} small />
                          <Link
                            className="ops-object-name"
                            to="/applications/$applicationId"
                            params={{ applicationId: app.id }}
                            search={{ service: name }}
                          >
                            {name}
                          </Link>
                        </div>
                        <div className="ops-object-id">
                          <code>{name}</code>
                          <Copy value={name} />
                        </div>
                        {runtime?.message && (
                          <small className="ops-table-sub">{runtime.message}</small>
                        )}
                      </td>
                      <td>
                        <code className="ops-image" title={runtime?.image || service.image}>
                          {runtime?.image || service.image}
                        </code>
                        <small className="ops-table-sub">
                          {runtime?.image ? 'Observed image' : 'Requested image'}
                        </small>
                      </td>
                      <td className="mono">
                        {runtime
                          ? `${runtime.ready} / ${runtime.desired} ready`
                          : `${service.replicas || 1} desired`}
                      </td>
                      <td>
                        {service.public
                          ? 'Public HTTP'
                          : service.port || service.ports?.length
                            ? 'Private'
                            : 'Worker'}
                        <small className="ops-table-sub mono">
                          {service.healthcheck || (service.port ? 'TCP probe' : 'Process health')}
                        </small>
                      </td>
                      <td className="mono">{service.size || 'small'}</td>
                      <td>
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
                        </Menu>
                      </td>
                    </tr>
                  )
                })}
              </tbody>
            </table>
          </div>
          {!observed.length && (
            <Note>
              No runtime observation has been recorded yet. Desired configuration above does not
              mean the services are running.
            </Note>
          )}
          <div className="resource-note">
            <Icon name="activity" size={18} />
            <div>
              <strong>Resource metrics</strong>
              <p>
                Open a service to inspect its live CPU, memory, pods, and events. Node capacity is
                available in Infrastructure.
              </p>
            </div>
            <Link to="/infrastructure">
              <Icon name="arrow" size={18} />
            </Link>
          </div>
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
              <h2>Networking</h2>
              <p>Service discovery, private networks, and public domains.</p>
            </div>
            <Link
              className="button button-secondary"
              to="/applications/$applicationId/domains"
              params={{ applicationId }}
            >
              Manage custom domains
              <Icon name="globe" size={14} />
            </Link>
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
          <h2>Deployment history</h2>
          <p>
            Immutable revisions, most recent first. Open a release for events and its configuration
            diff.
          </p>
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
                <th>Revision / state</th>
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
                          <span className="current-label">Current</span>
                        )}
                      </div>
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
