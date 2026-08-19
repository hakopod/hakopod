import { lazy, Suspense, useEffect, useState } from 'react'
import { createFileRoute, Link, useNavigate, useLocation, Outlet } from '@tanstack/react-router'
import { useQuery } from '@tanstack/react-query'
import * as Tabs from '@radix-ui/react-tabs'
import type { Application, DeploymentSummary } from '../lib/types'
import { message, relative, timestamp } from '../lib/api'
import { client, unwrap } from '../lib/client'
import { downloadConfig, specToTOML } from '../lib/toml'
import { useScope } from '../lib/scope'
import { Icon } from '../components/icons'
import { Button } from '../components/ui/button'
import { Dialog } from '../components/ui/dialog'
import { Copy, Empty, ErrorState, Loading, Note, Status } from '../components/shared'
import { Logs } from '../components/logs'
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
  const [tab, setTab] = useState(selectedTab || 'topology')
  useEffect(() => {
    if (selectedTab) setTab(selectedTab)
  }, [selectedTab])
  const [logService, setLogService] = useState('')
  const application = useQuery({
    queryKey: ['application', applicationId],
    queryFn: ({ signal }) =>
      unwrap(client.GET('/applications/{id}', { signal, params: { path: { id: applicationId } } })),
    refetchInterval: 10000,
    gcTime: 0,
  })
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
          key={selectedService}
          application={app}
          serviceName={selectedService}
          initialTab={selectedTab}
          initialPod={selectedPod}
        />
      </Suspense>
    )
  return (
    <>
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
            <h1>{app.name}</h1>
            <Status value={app.observed?.status || 'not observed'} />
          </div>
          <div className="application-metadata">
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
        <Tabs.List className="tab-list" aria-label="Application sections">
          {[
            ['topology', 'network', 'Topology'],
            ['services', 'box', 'Services'],
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
              <p>One application. Connected services. Independent health.</p>
            </div>
            <span className="label-chip">
              <Icon name="network" size={12} />
              Application private network
            </span>
          </div>
          <div className="service-detail-grid">
            {Object.entries(app.spec.services).map(([name, service]) => {
              const runtime = observed.find((status) => status.name === name)
              return (
                <div className="service-detail-card service-card-link" key={name}>
                  <Link
                    to="/applications/$applicationId"
                    params={{ applicationId: app.id }}
                    search={{ service: name }}
                    className="service-card-target"
                    aria-label={`Open ${name} service`}
                  />
                  <div className="service-detail-header">
                    <div className="service-mini-icon">
                      <Icon
                        name={service.public ? 'globe' : service.port ? 'box' : 'terminal'}
                        size={20}
                      />
                    </div>
                    <h3>{name}</h3>
                    <span className="form-spacer" />
                    <Status value={runtime?.status || 'not observed'} small />
                  </div>
                  <div className="service-detail-image">
                    <span>CONTAINER IMAGE</span>
                    <code title={runtime?.image || service.image}>
                      {runtime?.image || service.image}
                    </code>
                  </div>
                  <div className="service-facts">
                    <div>
                      <span>Exposure</span>
                      <strong>
                        <Icon name={service.public ? 'globe' : 'lock'} size={13} />
                        {service.public ? 'Public HTTP' : service.port ? 'Private' : 'Worker'}
                      </strong>
                    </div>
                    <div>
                      <span>Replicas</span>
                      <strong>
                        {runtime
                          ? `${runtime.ready} / ${runtime.desired} ready`
                          : `${service.replicas || 1} desired`}
                      </strong>
                    </div>
                    <div>
                      <span>Resource profile</span>
                      <strong>{service.size || 'small'}</strong>
                    </div>
                    <div>
                      <span>Readiness</span>
                      <strong className="mono">
                        {service.healthcheck || (service.port ? 'TCP probe' : 'Process health')}
                      </strong>
                    </div>
                  </div>
                  {runtime?.message && (
                    <div className="service-message">
                      <Icon name="info" size={14} />
                      {runtime.message}
                    </div>
                  )}
                  <div className="service-detail-footer">
                    {runtime?.url && /^https?:\/\//.test(runtime.url) ? (
                      <a href={runtime.url} target="_blank" rel="noreferrer">
                        <Icon name="external" size={13} />
                        Open service
                      </a>
                    ) : (
                      <span>
                        <Icon name="lock" size={13} />
                        {service.port ? `${name}:${service.port}` : 'No inbound port'}
                      </span>
                    )}
                    <Button
                      variant="ghost"
                      size="sm"
                      onClick={() => {
                        setLogService(name)
                        setTab('logs')
                      }}
                    >
                      <Icon name="terminal" size={13} />
                      View logs
                    </Button>
                  </div>
                </div>
              )
            })}
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
              <h2>Custom domains</h2>
              <p>Map verified hostnames to the application's public HTTP services.</p>
            </div>
            <Link
              className="button button-primary"
              to="/applications/$applicationId/domains"
              params={{ applicationId }}
            >
              Manage custom domains
              <Icon name="globe" size={14} />
            </Link>
          </div>
          <div className="section-toolbar">
            <div>
              <h2>Application networking</h2>
              <p>Service discovery and exposure from the applied specification.</p>
            </div>
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
                      {service.port ? (
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
                  variant="primary"
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
              <Copy value={specToTOML(app.spec)} label="Copy" />
            </div>
            <pre>{specToTOML(app.spec)}</pre>
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
    </>
  )
}

function DeploymentHistory({ application }: { application: Application }) {
  const scope = useScope()
  const deployments = application.deployments || []
  const [rollback, setRollback] = useState<DeploymentSummary | null>(null)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const navigate = useNavigate()
  const [requestKey, setRequestKey] = useState('')
  return (
    <>
      <div className="section-toolbar">
        <div>
          <h2>Deployment history</h2>
          <p>Every release is an immutable application revision.</p>
        </div>
        <span className="muted-text">Most recent first</span>
      </div>
      {!deployments.length ? (
        <Empty
          icon="branch"
          title="No deployments recorded"
          description="Accepted releases will appear here with their status and service results."
        />
      ) : (
        <div className="table-container">
          <table>
            <thead>
              <tr>
                <th>Revision</th>
                <th>Status</th>
                <th>Created</th>
                <th>Deployment</th>
                <th />
              </tr>
            </thead>
            <tbody>
              {[...deployments]
                .sort((a, b) => b.revision - a.revision)
                .map((deployment) => (
                  <tr key={deployment.id}>
                    <td>
                      <Link
                        to="/deployments/$deploymentId"
                        params={{ deploymentId: deployment.id }}
                        className="table-name"
                      >
                        <span className="revision-icon">
                          <Icon name="branch" size={14} />
                        </span>
                        r{deployment.revision}
                        {deployment.revision === application.revision && (
                          <span className="current-label">Current</span>
                        )}
                      </Link>
                    </td>
                    <td>
                      <Status value={deployment.status} small />
                    </td>
                    <td>{timestamp(deployment.created_at)}</td>
                    <td>
                      <code className="muted-text">{deployment.id.slice(0, 12)}</code>
                    </td>
                    <td className="align-right">
                      {scope.can('deployments:write') &&
                        deployment.status === 'succeeded' &&
                        deployment.revision < application.revision && (
                          <Button
                            size="sm"
                            variant="ghost"
                            onClick={() => {
                              setRollback(deployment)
                              setError('')
                              setRequestKey(crypto.randomUUID())
                            }}
                          >
                            Roll back
                          </Button>
                        )}
                      <Button asChild size="icon" variant="ghost">
                        <Link
                          to="/deployments/$deploymentId"
                          params={{ deploymentId: deployment.id }}
                          aria-label={`View revision ${deployment.revision}`}
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
      <Dialog
        open={Boolean(rollback)}
        onOpenChange={(open) => {
          if (!busy && !open) setRollback(null)
        }}
        title={`Roll back to revision ${rollback?.revision || ''}`}
        description="Create a new, auditable release from the selected revision’s resolved image digests."
      >
        <div className="dialog-body">
          <Note>
            This restores the recorded service configuration. It does not reverse database
            migrations or restore historical values from external secret providers.
          </Note>
          {error && <div className="inline-error">{error}</div>}
        </div>
        <div className="dialog-footer">
          <Button disabled={busy} onClick={() => setRollback(null)}>
            Cancel
          </Button>
          <Button
            variant="primary"
            disabled={busy}
            onClick={async () => {
              if (!rollback) return
              setBusy(true)
              setError('')
              try {
                const accepted = await unwrap(
                  client.POST('/applications/{id}/rollback', {
                    params: {
                      path: { id: application.id },
                      header: { 'Idempotency-Key': requestKey },
                    },
                    body: { revision: rollback.revision, expected_revision: application.revision },
                  }),
                )
                setRollback(null)
                void navigate({
                  to: '/deployments/$deploymentId',
                  params: { deploymentId: accepted.id },
                })
              } catch (err) {
                setError(message(err))
              } finally {
                setBusy(false)
              }
            }}
          >
            {busy ? 'Submitting…' : 'Create rollback deployment'}
          </Button>
        </div>
      </Dialog>
    </>
  )
}
