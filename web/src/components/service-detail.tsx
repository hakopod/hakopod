import { lazy, Suspense, useEffect, useState } from 'react'
import { Link, useNavigate } from '@tanstack/react-router'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import * as Tabs from '@radix-ui/react-tabs'
import type { Application } from '../lib/types'
import { client, unwrap } from '../lib/client'
import { message, relative, timestamp } from '../lib/api'
import { Menu, MenuItem } from '@hakopod/hatch-ui/components/dropdown-menu'
import { specToTOML } from '../lib/toml'
import { useScope } from '../lib/scope'
import { Button } from './ui/button'
import { Dialog } from './ui/dialog'
import { Icon } from './icons'
import { Copy, Empty, ErrorState, Loading, Note, Status } from './shared'
import { Logs } from './logs'

const PodTerminal = lazy(() => import('./pod-terminal'))
const ServiceTLS = lazy(() => import('./tls-settings').then((m) => ({ default: m.ServiceTLS })))

const readRuntime = (applicationId: string, service: string, signal: AbortSignal) =>
  unwrap(
    client.GET('/applications/{id}/services/{service}/runtime', {
      signal,
      params: { path: { id: applicationId, service } },
    }),
  )
type Runtime = Awaited<ReturnType<typeof readRuntime>>
type Sample = { at: string; cpu: number; memory: number }

function memory(bytes?: number) {
  if (bytes === undefined) return 'Unavailable'
  return bytes >= 1024 ** 3
    ? `${(bytes / 1024 ** 3).toFixed(2)} GiB`
    : `${(bytes / 1024 ** 2).toFixed(1)} MiB`
}

function MetricChart({
  samples,
  field,
  label,
}: {
  samples: Sample[]
  field: 'cpu' | 'memory'
  label: string
}) {
  if (samples.length < 2) return <p className="metric-wait">Collecting a second observed sample…</p>
  const maximum = Math.max(1, ...samples.map((sample) => sample[field]))
  const start = Date.parse(samples[0].at)
  const duration = Math.max(1, Date.parse(samples[samples.length - 1].at) - start)
  const path = samples
    .map(
      (sample, index) =>
        `${index ? 'L' : 'M'}${8 + ((Date.parse(sample.at) - start) / duration) * 304},${72 - (sample[field] / maximum) * 58}`,
    )
    .join(' ')
  return (
    <svg
      className="metric-chart"
      viewBox="0 0 320 80"
      role="img"
      aria-label={`${label}, ${samples.length} actual samples from ${timestamp(samples[0].at)} to ${timestamp(samples[samples.length - 1].at)}`}
    >
      <path d="M8 73H312" className="metric-baseline" />
      <path
        d={path}
        fill="none"
        stroke="currentColor"
        strokeWidth="2"
        vectorEffect="non-scaling-stroke"
      />
    </svg>
  )
}

export function ServiceDetail({
  application,
  serviceName,
  initialTab,
  initialPod,
}: {
  application: Application
  serviceName: string
  initialTab?: string
  initialPod?: string
}) {
  const scope = useScope()
  const navigate = useNavigate()
  const cache = useQueryClient()
  const service = application.spec.services[serviceName]
  const observed = application.observed?.services?.find((item) => item.name === serviceName)
  const [tab, setTab] = useState(
    initialTab &&
      ['overview', 'pods', 'logs', 'terminal', 'network', 'settings'].includes(initialTab)
      ? initialTab
      : 'overview',
  )
  const [terminalPod, setTerminalPod] = useState(initialPod || '')
  useEffect(() => {
    if (
      initialTab &&
      ['overview', 'pods', 'logs', 'terminal', 'network', 'settings'].includes(initialTab)
    )
      setTab(initialTab)
  }, [initialTab])
  const [restartOpen, setRestartOpen] = useState(false)
  const [requestKey, setRequestKey] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [samples, setSamples] = useState<Sample[]>([])
  const runtime = useQuery({
    queryKey: ['service-runtime', application.id, serviceName],
    queryFn: ({ signal }) => readRuntime(application.id, serviceName, signal),
    enabled: Boolean(service),
    refetchInterval: 15000,
    refetchIntervalInBackground: false,
    gcTime: 0,
  })
  useEffect(() => {
    const metrics = runtime.data?.metrics
    if (
      !metrics?.available ||
      metrics.cpu_millicores === undefined ||
      metrics.memory_bytes === undefined
    )
      return
    const at = metrics.sampled_at || runtime.data?.observed_at
    if (!at || !Number.isFinite(Date.parse(at))) return
    const sample = { at, cpu: metrics.cpu_millicores, memory: metrics.memory_bytes }
    setSamples((previous) =>
      previous[previous.length - 1]?.at === at ? previous : [...previous.slice(-23), sample],
    )
  }, [runtime.data])
  const back = (
    <Link
      to="/applications/$applicationId"
      params={{ applicationId: application.id }}
      search={{}}
      className="back-link"
    >
      <Icon name="back" size={14} />
      {application.name} / Services
    </Link>
  )
  if (!service)
    return (
      <>
        {back}
        <Empty
          icon="box"
          title="Service not found"
          description="This service is not part of the application's current configuration."
        />
      </>
    )
  const metrics = runtime.data?.metrics
  const edit = (mode: 'form' | 'toml') => {
    void navigate({
      to: '/applications/$applicationId/configure',
      params: { applicationId: application.id },
      search: { mode, service: serviceName },
    })
  }
  return (
    <div className="ops-page ops-service-page">
      {back}
      <div className="application-heading">
        <div className="app-symbol app-symbol-large">
          <Icon name={service.public ? 'globe' : service.port ? 'box' : 'terminal'} size={27} />
        </div>
        <div>
          <div className="title-row">
            <Status value={observed?.status || 'not observed'} />
            <h1>{serviceName}</h1>
          </div>
          <div className="application-metadata">
            <code>{serviceName}</code>
            <Copy value={serviceName} />
            <span>
              {application.project} / {application.environment} / {application.name}
            </span>
            <span>Revision {application.revision}</span>
          </div>
        </div>
        <div className="form-spacer" />
        {scope.can('deployments:write') && (
          <div className="toolbar-actions">
            <Button
              onClick={() => {
                setRequestKey(crypto.randomUUID())
                setError('')
                setRestartOpen(true)
              }}
            >
              <Icon name="refresh" size={14} />
              Restart
            </Button>
            <Button
              variant={['logs', 'terminal', 'settings'].includes(tab) ? 'secondary' : 'primary'}
              onClick={() => edit('form')}
            >
              <Icon name="settings" size={14} />
              Stage changes
            </Button>
          </div>
        )}
      </div>
      {observed?.message && <Note>{observed.message}</Note>}
      <Tabs.Root value={tab} onValueChange={setTab}>
        <Tabs.List className="tab-list" aria-label="Service sections">
          {[
            ['overview', 'activity', 'Overview'],
            ['pods', 'box', 'Pods'],
            ['logs', 'activity', 'Logs'],
            ['terminal', 'terminal', 'Terminal'],
            ['network', 'network', 'Networking'],
            ['settings', 'settings', 'Settings'],
          ].map(([value, icon, label]) => (
            <Tabs.Trigger className="tab-trigger" key={value} value={value}>
              <Icon name={icon} size={15} />
              {label}
            </Tabs.Trigger>
          ))}
        </Tabs.List>
        <Tabs.Content value="overview" className="tab-content">
          <div className="service-overview-grid">
            <section className="panel service-summary-panel">
              <div className="panel-heading">
                <h2>Runtime</h2>
                <span className="label-chip">{service.size || 'small'} profile</span>
              </div>
              <dl className="service-definition-list">
                <div>
                  <dt>Replicas</dt>
                  <dd>
                    {observed
                      ? `${observed.ready} / ${observed.desired} ready`
                      : `${service.replicas || 1} desired · not observed`}
                  </dd>
                </div>
                <div>
                  <dt>Readiness</dt>
                  <dd>{service.healthcheck || (service.port ? 'TCP probe' : 'Process health')}</dd>
                </div>
                <div>
                  <dt>Exposure</dt>
                  <dd>
                    {service.public
                      ? 'Public HTTP'
                      : service.port
                        ? 'Application private network'
                        : 'No inbound port'}
                  </dd>
                </div>
                <div>
                  <dt>Container image</dt>
                  <dd className="mono break-text">{observed?.image || service.image}</dd>
                </div>
                <div>
                  <dt>Observed</dt>
                  <dd>{timestamp(runtime.data?.observed_at)}</dd>
                </div>
              </dl>
            </section>
            <section className="panel service-summary-panel">
              <div className="panel-heading">
                <h2>Resource usage</h2>
                <Button
                  size="icon"
                  variant="ghost"
                  aria-label="Refresh service runtime"
                  onClick={() => void runtime.refetch()}
                >
                  <Icon name="refresh" size={15} />
                </Button>
              </div>
              {runtime.isPending ? (
                <Loading rows={2} />
              ) : runtime.error ? (
                <ErrorState error={runtime.error} retry={() => void runtime.refetch()} />
              ) : !metrics?.available ? (
                <Empty
                  icon="activity"
                  title="Metrics unavailable"
                  description={
                    metrics?.reason || 'The cluster has not returned a current resource sample.'
                  }
                />
              ) : (
                <>
                  <div className="service-metrics">
                    <div>
                      <span>CPU</span>
                      <strong>
                        {metrics.cpu_millicores === undefined
                          ? 'Unavailable'
                          : `${metrics.cpu_millicores.toFixed(1)} mCPU`}
                      </strong>
                      <MetricChart samples={samples} field="cpu" label="CPU usage" />
                    </div>
                    <div>
                      <span>Memory</span>
                      <strong>{memory(metrics.memory_bytes)}</strong>
                      <MetricChart samples={samples} field="memory" label="Memory usage" />
                    </div>
                  </div>
                  <p className="field-help">
                    {metrics.pods_sampled} / {metrics.pods_expected} pods sampled ·{' '}
                    {timestamp(metrics.sampled_at)}. Up to 24 actual samples retained while this
                    view is open.
                  </p>
                </>
              )}
            </section>
          </div>
          <div className="section-toolbar">
            <div>
              <h2>Pods</h2>
              <p>Live workloads owned by this service.</p>
            </div>
            <Button size="sm" onClick={() => setTab('pods')}>
              Inspect all pods
              <Icon name="arrow" size={14} />
            </Button>
          </div>
          <PodList
            runtime={runtime.data}
            loading={runtime.isPending}
            error={runtime.error}
            compact
          />
        </Tabs.Content>
        <Tabs.Content value="pods" className="tab-content">
          <div className="section-toolbar">
            <div>
              <h2>Pods and events</h2>
              <p>Container state, allocations, readiness conditions, and Kubernetes events.</p>
            </div>
            <Button size="sm" onClick={() => void runtime.refetch()}>
              <Icon name="refresh" size={14} />
              Refresh
            </Button>
          </div>
          <PodList
            runtime={runtime.data}
            loading={runtime.isPending}
            error={runtime.error}
            onConnect={
              scope.can('deployments:write')
                ? (pod) => {
                    setTerminalPod(pod)
                    setTab('terminal')
                  }
                : undefined
            }
          />
        </Tabs.Content>
        <Tabs.Content value="terminal" className="tab-content">
          <Suspense fallback={<Loading />}>
            <PodTerminal
              key={terminalPod}
              applicationId={application.id}
              services={[serviceName]}
              initialService={serviceName}
              initialPod={terminalPod}
            />
          </Suspense>
        </Tabs.Content>
        <Tabs.Content value="logs" className="tab-content">
          {scope.can('logs:read') ? (
            <Logs
              applicationId={application.id}
              services={[serviceName]}
              initialService={serviceName}
            />
          ) : (
            <Empty
              icon="lock"
              title="Logs need additional permission"
              description="Your account needs logs:read for this application."
            />
          )}
        </Tabs.Content>
        <Tabs.Content value="network" className="tab-content">
          <section className="panel service-summary-panel">
            <div className="panel-heading">
              <h2>Service networking</h2>
            </div>
            <dl className="service-definition-list">
              <div>
                <dt>Exposure</dt>
                <dd>
                  {service.public
                    ? 'Public HTTP'
                    : service.port
                      ? 'Private service'
                      : 'Background worker'}
                </dd>
              </div>
              <div>
                <dt>Private address</dt>
                <dd>
                  {service.port ? (
                    <span className="copyable-address">
                      <code>{observed?.internal_address || `${serviceName}:${service.port}`}</code>
                      <Copy
                        value={observed?.internal_address || `${serviceName}:${service.port}`}
                      />
                    </span>
                  ) : (
                    'No inbound port'
                  )}
                </dd>
              </div>
              <div>
                <dt>Public endpoint</dt>
                <dd>
                  {observed?.url && /^https?:\/\//.test(observed.url) ? (
                    <a href={observed.url} target="_blank" rel="noreferrer">
                      {observed.url}
                      <Icon name="external" size={13} />
                    </a>
                  ) : (
                    'No public endpoint observed'
                  )}
                </dd>
              </div>
              <div>
                <dt>Networks</dt>
                <dd>
                  {(service.networks?.length ? service.networks : ['default']).map((name) => (
                    <span className="label-chip" key={name}>
                      {name}
                      {application.spec.networks?.[name]?.internal && ' · internal'}
                    </span>
                  ))}
                </dd>
              </div>
              <div>
                <dt>Readiness dependencies</dt>
                <dd>
                  {service.depends_on?.length
                    ? service.depends_on.map((name) => (
                        <Link
                          key={name}
                          to="/applications/$applicationId"
                          params={{ applicationId: application.id }}
                          search={{ service: name }}
                        >
                          {name}
                          <Icon name="chevron" size={12} />
                        </Link>
                      ))
                    : 'None configured'}
                </dd>
              </div>
            </dl>
          </section>
          <Note>
            Private addresses are reachable by permitted workloads in this application. Browser
            requests use the public endpoint or a server-side proxy.
          </Note>
          <Suspense fallback={<Loading rows={2} />}>
            <ServiceTLS application={application} service={serviceName} />
          </Suspense>
        </Tabs.Content>
        <Tabs.Content value="settings" className="tab-content">
          <div className="section-toolbar">
            <div>
              <h2>Applied service configuration</h2>
              <p>Changes create a reviewed, immutable revision for this application.</p>
            </div>
            {scope.can('deployments:write') && (
              <Button variant="primary" onClick={() => edit('toml')}>
                <Icon name="code" size={14} />
                Edit configuration
              </Button>
            )}
          </div>
          {service.autoscaling && (
            <Note>Replica count is managed by this service's autoscaling configuration.</Note>
          )}
          <div className="code-panel">
            <div>
              <span>
                <Icon name="code" size={14} />
                {serviceName} · configuration excerpt
              </span>
            </div>
            <pre>
              {specToTOML({
                schema_version: application.spec.schema_version,
                name: application.name,
                services: { [serviceName]: service },
              })}
            </pre>
          </div>
        </Tabs.Content>
      </Tabs.Root>
      <Dialog
        open={restartOpen}
        onOpenChange={(value) => {
          if (!busy) setRestartOpen(value)
        }}
        title={`Restart ${serviceName}?`}
        description={`Create a new application revision that restarts only ${serviceName} in ${application.project} / ${application.environment}.`}
      >
        <div className="dialog-body">
          <Note>
            Current revision: r{application.revision}. Replacement pods must pass readiness before
            they receive traffic.
          </Note>
          {error && (
            <div className="inline-error" role="alert">
              {error}
            </div>
          )}
        </div>
        <div className="dialog-footer">
          <Button disabled={busy} onClick={() => setRestartOpen(false)}>
            Cancel
          </Button>
          <Button
            variant="danger"
            disabled={busy}
            onClick={async () => {
              setBusy(true)
              setError('')
              try {
                const result = await unwrap(
                  client.POST('/applications/{id}/services/{service}/restart', {
                    params: {
                      path: { id: application.id, service: serviceName },
                      header: { 'Idempotency-Key': requestKey },
                    },
                    body: { expected_revision: application.revision },
                  }),
                )
                void cache.invalidateQueries({ queryKey: ['application', application.id] })
                setRestartOpen(false)
                void navigate({
                  to: '/deployments/$deploymentId',
                  params: { deploymentId: result.id },
                })
              } catch (err) {
                setError(message(err))
              } finally {
                setBusy(false)
              }
            }}
          >
            {busy ? 'Submitting…' : 'Restart service'}
          </Button>
        </div>
      </Dialog>
    </div>
  )
}

function PodList({
  runtime,
  loading,
  error,
  compact = false,
  onConnect,
}: {
  runtime?: Runtime
  loading: boolean
  error: unknown
  compact?: boolean
  onConnect?: (pod: string) => void
}) {
  const [inspected, setInspected] = useState('')
  const pod = runtime?.pods.find((item) => item.name === inspected)
  if (loading) return <Loading rows={2} />
  if (error) return <ErrorState error={error} />
  if (!runtime?.pods.length)
    return (
      <Empty
        icon="box"
        title="No pods observed"
        description="No owned pods were returned for this service."
      />
    )
  return (
    <>
      <div className="table-container ops-table">
        <table>
          <thead>
            <tr>
              <th>Pod / state</th>
              <th>Restarts</th>
              <th>CPU / memory</th>
              <th>Age</th>
              <th>Node</th>
              <th>
                <span className="sr-only">Pod actions</span>
              </th>
            </tr>
          </thead>
          <tbody>
            {runtime.pods.slice(0, compact ? 6 : 40).map((item) => {
              const cpuKnown =
                item.containers.length > 0 &&
                item.containers.every((container) => container.cpu_millicores !== undefined)
              const memoryKnown =
                item.containers.length > 0 &&
                item.containers.every((container) => container.memory_bytes !== undefined)
              const crash = item.containers.some(
                (container) => container.reason === 'CrashLoopBackOff',
              )
              return (
                <tr key={item.name}>
                  <td>
                    <div className="ops-object">
                      <Status
                        value={crash ? 'CrashLoop' : item.ready ? 'ready' : item.phase}
                        small
                      />
                      <button
                        type="button"
                        className="ops-object-name"
                        onClick={() => setInspected(item.name)}
                      >
                        {item.name}
                      </button>
                    </div>
                  </td>
                  <td className="mono">
                    {item.containers.reduce((n, container) => n + container.restarts, 0)}
                  </td>
                  <td className="mono">
                    {cpuKnown
                      ? `${item.containers.reduce((n, container) => n + (container.cpu_millicores || 0), 0).toFixed(1)} mCPU`
                      : 'Unavailable'}
                    <small className="ops-table-sub">
                      {memoryKnown
                        ? memory(
                            item.containers.reduce(
                              (n, container) => n + (container.memory_bytes || 0),
                              0,
                            ),
                          )
                        : 'Memory unavailable'}
                    </small>
                  </td>
                  <td>
                    <time dateTime={item.created_at} title={item.created_at}>
                      {relative(item.created_at)}
                    </time>
                  </td>
                  <td>
                    <code>{item.node_name || 'Not assigned'}</code>
                  </td>
                  <td>
                    <Menu
                      trigger={
                        <Button variant="ghost" size="icon" aria-label={`Actions for ${item.name}`}>
                          <span aria-hidden="true">···</span>
                        </Button>
                      }
                    >
                      <MenuItem onSelect={() => setInspected(item.name)}>
                        <Icon name="box" size={14} />
                        Inspect pod
                      </MenuItem>
                      {onConnect && (
                        <MenuItem
                          disabled={item.phase !== 'Running'}
                          onSelect={() => onConnect(item.name)}
                        >
                          <Icon name="terminal" size={14} />
                          Open terminal
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
      {(runtime.truncated || (!compact && runtime.pods.length > 40)) && (
        <Note>This response is limited. Inspect the cluster for additional pods or events.</Note>
      )}
      <Dialog
        open={Boolean(pod)}
        onOpenChange={(open) => {
          if (!open) setInspected('')
        }}
        title={pod?.name || 'Pod'}
        description="Observed containers, allocations, conditions, and recent events."
        sheet
      >
        {pod && (
          <>
            <div className="dialog-body ops-pod-inspector">
              <div className="ops-object">
                <Status value={pod.ready ? 'ready' : pod.phase} />
                <Copy value={pod.name} label="Copy pod name" />
              </div>
              <dl className="service-definition-list">
                <div>
                  <dt>Node</dt>
                  <dd className="mono">{pod.node_name || 'Not assigned'}</dd>
                </div>
                <div>
                  <dt>Pod IP</dt>
                  <dd>
                    {pod.pod_ip ? (
                      <span className="copyable-address">
                        <code>{pod.pod_ip}</code>
                        <Copy value={pod.pod_ip} />
                      </span>
                    ) : (
                      'Not assigned'
                    )}
                  </dd>
                </div>
                <div>
                  <dt>Created</dt>
                  <dd>
                    <time title={pod.created_at}>{timestamp(pod.created_at)}</time>
                  </dd>
                </div>
                <div>
                  <dt>Observed</dt>
                  <dd>{timestamp(runtime.observed_at)}</dd>
                </div>
              </dl>
              <h3>Containers</h3>
              {pod.containers.map((container) => (
                <section className="ops-inspector-section" key={container.name}>
                  <div className="ops-object">
                    <Status value={container.ready ? 'ready' : container.state} small />
                    <h3>{container.name}</h3>
                  </div>
                  {(container.reason || container.message) && (
                    <p className="field-help">
                      {container.reason}
                      {container.message ? ` · ${container.message}` : ''}
                    </p>
                  )}
                  <dl className="service-definition-list">
                    <div>
                      <dt>Readiness / restarts</dt>
                      <dd>
                        {container.ready ? 'Ready' : 'Not ready'} · {container.restarts} restarts
                      </dd>
                    </div>
                    <div>
                      <dt>CPU request / limit</dt>
                      <dd className="mono">
                        {container.resources.requests.cpu || 'Unspecified'} /{' '}
                        {container.resources.limits.cpu || 'Unspecified'}
                      </dd>
                    </div>
                    <div>
                      <dt>Memory request / limit</dt>
                      <dd className="mono">
                        {container.resources.requests.memory || 'Unspecified'} /{' '}
                        {container.resources.limits.memory || 'Unspecified'}
                      </dd>
                    </div>
                    <div>
                      <dt>Observed usage</dt>
                      <dd className="mono">
                        {container.cpu_millicores === undefined
                          ? 'CPU unavailable'
                          : `${container.cpu_millicores.toFixed(1)} mCPU`}{' '}
                        / {memory(container.memory_bytes)}
                      </dd>
                    </div>
                    <div>
                      <dt>Image</dt>
                      <dd>
                        <code className="break-text">{container.image_id || container.image}</code>
                        <Copy value={container.image_id || container.image} label="Copy image" />
                      </dd>
                    </div>
                  </dl>
                </section>
              ))}
              <h3>Conditions</h3>
              {pod.conditions.length ? (
                <div className="ops-event-list">
                  {pod.conditions.map((condition) => (
                    <div key={condition.type}>
                      <div className="ops-object">
                        <Status value={condition.status === 'True' ? 'ready' : 'pending'} small />
                        <strong>{condition.type}</strong>
                      </div>
                      <p>
                        {condition.reason}
                        {condition.message ? ` · ${condition.message}` : ''}
                      </p>
                      <time title={condition.last_transition_time}>
                        {timestamp(condition.last_transition_time)}
                      </time>
                    </div>
                  ))}
                </div>
              ) : (
                <p className="field-help">No conditions returned.</p>
              )}
              <h3>Recent events</h3>
              {pod.events.length ? (
                <div className="ops-event-list">
                  {pod.events.slice(-12).map((event, index) => (
                    <div key={`${event.reason}-${index}`}>
                      <div className="ops-object">
                        <Icon name={event.type === 'Warning' ? 'alert' : 'info'} size={14} />
                        <strong>{event.reason}</strong>
                        <span className="muted-text">×{event.count}</span>
                      </div>
                      <p>{event.message}</p>
                      <time title={event.last_seen}>{timestamp(event.last_seen)}</time>
                    </div>
                  ))}
                </div>
              ) : (
                <p className="field-help">No recent events returned.</p>
              )}
            </div>
            <div className="dialog-footer">
              <Button variant="ghost" onClick={() => setInspected('')}>
                Close
              </Button>
              {onConnect && (
                <Button
                  variant="primary"
                  disabled={pod.phase !== 'Running'}
                  onClick={() => {
                    onConnect(pod.name)
                    setInspected('')
                  }}
                >
                  <Icon name="terminal" size={14} />
                  Open terminal
                </Button>
              )}
            </div>
          </>
        )}
      </Dialog>
    </>
  )
}
