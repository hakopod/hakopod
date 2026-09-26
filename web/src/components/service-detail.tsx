import { VolumeResizeButton } from './volume-resize'
import { PublicEndpoints } from './public-endpoints'
import type { PublicEndpoint } from '../lib/public-endpoints'
import { serviceProfileLabel } from '../lib/service-resources'
import { MoveServiceDialog } from './move-service-dialog'
import { effectiveService } from '../lib/effective-service'
import { RenameResource } from './rename-resource'
import { ManagedActionsStatus } from './managed-actions-status'
import { useEditionFeatures } from '../lib/dashboard-edition'
import { lazy, Suspense, useEffect, useRef, useState } from 'react'
import { Link, useNavigate } from '@tanstack/react-router'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import * as Tabs from '@radix-ui/react-tabs'
import type { Application, Service, Spec } from '../lib/types'
import { client, unwrap } from '../lib/client'
import { message, relative, timestamp } from '../lib/api'
import { Menu, MenuItem } from '@hakopod/hatch-ui/components/dropdown-menu'
import { readinessLabel } from '../lib/readiness'
import { specToTOML } from '../lib/toml'
import { useScope } from '../lib/scope'
import { useActiveSection } from '../lib/use-active-section'
import { runtimeReplicaSummary, serviceRuntimeHealth } from '../lib/runtime-health'
import { ApplicationAlarmLinks, RuntimeNotice } from './runtime-notice'
import {
  metricSampleAge,
  metricsStaleAfter,
  retainMetricSample,
  retainedMetricSamples,
  type MetricSample,
} from '../lib/runtime-metrics'
import { Button } from './ui/button'
import { Dialog } from './ui/dialog'
import { Badge } from './ui/surfaces'
import { Icon } from './icons'
import { HeadingHelp, Copy, Empty, ErrorState, Loading, Note, Status, RequestError } from './shared'
import { Logs } from './logs'
import { Requests } from './requests'
import { ResourceMetric, validMetricUsage } from './resource-metric'

const PodTerminal = lazy(() => import('./pod-terminal'))
const ServiceSecrets = lazy(() =>
  import('./service-secrets').then((m) => ({ default: m.ServiceSecrets })),
)
const serviceTabs = [
  'overview',
  'pods',
  'logs',
  'requests',
  'environment',
  'secrets',
  'terminal',
  'network',
  'settings',
]
const ServiceDelivery = lazy(() =>
  import('./service-delivery').then((m) => ({ default: m.ServiceDelivery })),
)
const ServiceTLS = lazy(() => import('./tls-settings').then((m) => ({ default: m.ServiceTLS })))

const readRuntime = (applicationId: string, service: string, signal: AbortSignal) =>
  unwrap(
    client.GET('/applications/{id}/services/{service}/runtime', {
      signal,
      params: { path: { id: applicationId, service } },
    }),
  )
type Runtime = Awaited<ReturnType<typeof readRuntime>>
const sampleTimeFormat = new Intl.DateTimeFormat(undefined, {
  month: 'short',
  day: 'numeric',
  hour: '2-digit',
  minute: '2-digit',
  second: '2-digit',
})

function sampleTimestamp(value: string) {
  const time = Date.parse(value)
  return Number.isFinite(time) ? sampleTimeFormat.format(time) : 'Unavailable'
}

function memory(bytes?: number) {
  if (bytes === undefined) return 'Unavailable'
  return bytes >= 1024 ** 3
    ? `${(bytes / 1024 ** 3).toFixed(2)} GiB`
    : `${(bytes / 1024 ** 2).toFixed(1)} MiB`
}

export function ServiceDetail({
  application,
  endpoints,
  serviceName,
  initialTab,
  initialPod,
}: {
  application: Application
  endpoints: PublicEndpoint[]
  serviceName: string
  initialTab?: string
  initialPod?: string
}) {
  const features = useEditionFeatures()
  const scope = useScope()
  const navigate = useNavigate()
  const cache = useQueryClient()
  const service = application.spec.services[serviceName]
  const observed = application.observed?.services?.find((item) => item.name === serviceName)
  const health = serviceRuntimeHealth(observed, application.observed?.observed_at)
  const [moving, setMoving] = useState(false)
  const serviceActionsTrigger = useRef<HTMLButtonElement>(null)
  const [tab, setTab] = useState(
    initialTab && serviceTabs.includes(initialTab) ? initialTab : 'overview',
  )
  const [terminalPod, setTerminalPod] = useState(initialPod || '')
  const navigationRoot = useActiveSection(tab, '.tab-list')
  useEffect(() => {
    if (initialTab && serviceTabs.includes(initialTab)) setTab(initialTab)
  }, [initialTab])
  const [restartOpen, setRestartOpen] = useState(false)
  const [requestKey, setRequestKey] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [samples, setSamples] = useState<MetricSample[]>([])
  const [paused, setPaused] = useState(false)
  const [visible, setVisible] = useState(false)
  const [now, setNow] = useState(Date.now)
  const runtimeTab = tab === 'overview' || tab === 'pods'
  const polling = Boolean(service) && visible && runtimeTab && !paused
  useEffect(() => {
    const change = () => {
      setVisible(!document.hidden)
      setNow(Date.now())
    }
    document.addEventListener('visibilitychange', change)
    change()
    return () => document.removeEventListener('visibilitychange', change)
  }, [])
  useEffect(() => {
    if (!visible || tab !== 'overview') return
    setNow(Date.now())
    const timer = setInterval(() => setNow(Date.now()), 5000)
    return () => clearInterval(timer)
  }, [visible, tab])
  const runtime = useQuery({
    queryKey: ['service-runtime', application.id, serviceName],
    queryFn: ({ signal }) => readRuntime(application.id, serviceName, signal),
    enabled: polling,
    refetchInterval: polling ? 15000 : false,
    refetchIntervalInBackground: false,
    refetchOnWindowFocus: false,
    retry: false,
    gcTime: 0,
  })
  useEffect(() => {
    if (!polling)
      void cache.cancelQueries({
        queryKey: ['service-runtime', application.id, serviceName],
        exact: true,
      })
  }, [polling, visible, runtimeTab, cache, application.id, serviceName])
  useEffect(() => {
    setSamples((previous) => retainMetricSample(previous, runtime.data?.metrics))
  }, [runtime.data])
  if (!service)
    return (
      <>
        <Empty
          icon="box"
          title="Service not found"
          description="This service is not part of the application's current configuration."
        />
      </>
    )
  const effective = effectiveService(application.spec, service)
  const metrics = runtime.data?.metrics
  const hasPorts = Boolean(service.port || service.ports?.length)
  const sampleAge = metricSampleAge(
    metrics?.sampled_at,
    runtime.data?.observed_at,
    runtime.dataUpdatedAt,
    now,
  )
  const available =
    metrics?.available &&
    validMetricUsage(metrics.cpu_millicores) &&
    validMetricUsage(metrics.memory_bytes)
  const fresh = available && sampleAge !== null && sampleAge <= metricsStaleAfter
  const freshness = runtime.error
    ? 'Check failed'
    : !available
      ? 'No sample'
      : sampleAge === null
        ? 'Freshness unknown'
        : !fresh
          ? 'Stale sample'
          : polling
            ? 'Live'
            : 'Paused'
  const edit = (mode: 'form' | 'toml') => {
    if (service.actions && mode === 'form') {
      void navigate({
        to: '/templates/$templateId',
        params: { templateId: 'managed-actions' },
        search: { application: application.id, runner: serviceName },
      })
      return
    }
    void navigate({
      to: '/applications/$applicationId/configure',
      params: { applicationId: application.id },
      search: { mode, ...(mode === 'form' ? { service: serviceName } : {}) },
    })
  }
  return (
    <div className="ops-page ops-service-page">
      {moving && (
        <MoveServiceDialog
          application={application}
          service={serviceName}
          restoreFocus={() => serviceActionsTrigger.current?.focus()}
          onClose={() => setMoving(false)}
        />
      )}
      <div className="application-heading">
        <div>
          <div className="title-row hako-page-heading-title">
            <Status value={health.status} />
            <div className="flex min-w-0 max-w-full items-center gap-2">
              <h1 className="min-w-0! flex-initial! break-words">
                {application.service_display_names?.[serviceName] || serviceName}
              </h1>
              <RenameResource application={application} service={serviceName} />
            </div>
          </div>
          <div className="application-metadata">
            <span className="min-w-0 max-w-full break-all">
              {application.display_name || application.name}
            </span>
            <span>Revision {application.revision}</span>
          </div>
        </div>
        <div className="form-spacer" />
        {scope.can('deployments:write') && (
          <div className="toolbar-actions">
            <Menu
              trigger={
                <Button
                  variant="ghost"
                  size="icon"
                  aria-label="Service actions"
                  ref={serviceActionsTrigger}
                >
                  <span aria-hidden="true">···</span>
                </Button>
              }
            >
              <MenuItem onSelect={() => setMoving(true)}>
                <Icon name="arrow" size={14} />
                Move to application
              </MenuItem>
            </Menu>
            {!service.job && (
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
            )}
            <Button
              variant={
                ['logs', 'environment', 'secrets', 'terminal', 'settings'].includes(tab)
                  ? 'secondary'
                  : 'primary'
              }
              onClick={() => edit('form')}
            >
              <Icon name="settings" size={14} />
              Stage changes
            </Button>
          </div>
        )}
      </div>
      <RuntimeNotice
        job={Boolean(service.job)}
        health={health}
        applicationId={application.id}
        canInspectNodes={scope.can('admin')}
        canInspectLogs={scope.can('logs:read')}
      />
      {observed?.message && !health.issues.length && !health.note && (
        <Note>{observed.message}</Note>
      )}
      <Tabs.Root
        value={tab === 'terminal' && !features.terminal ? 'overview' : tab}
        onValueChange={(value) => {
          setTab(value)
          void navigate({
            to: '/applications/$applicationId',
            params: { applicationId: application.id },
            search: {
              service: serviceName,
              tab: value,
              ...(value === 'terminal' && terminalPod ? { pod: terminalPod } : {}),
            },
            replace: true,
          })
        }}
      >
        <Tabs.List
          ref={navigationRoot}
          className="tab-list application-tabs"
          aria-label="Service sections"
        >
          {[
            ['overview', 'activity', 'Overview'],
            ['pods', 'box', 'Pods'],
            ['logs', 'activity', 'Logs'],
            ['requests', 'activity', 'Requests'],
            ['environment', 'code', 'Environment'],
            ['secrets', 'lock', 'Secrets'],
            ['terminal', 'terminal', 'Terminal'],
            ['network', 'network', 'Networking'],
            ['settings', 'settings', 'Settings'],
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
        <Tabs.Content value="requests" className="tab-content">
          {scope.can('logs:read') ? (
            <Requests key={serviceName} applicationId={application.id} service={serviceName} />
          ) : (
            <Empty
              title="Request logs need additional permission"
              description="Ask an administrator for logs:read access to this application."
            />
          )}
        </Tabs.Content>
        <Tabs.Content value="overview" className="tab-content">
          {service.job?.schedule && (
            <section className="panel service-summary-panel mb-4">
              <div className="panel-heading">
                <h2>Recent scheduled runs</h2>
              </div>
              {application.observed.observed_at && (
                <p className="px-4 text-sm muted-text">
                  Last snapshot: {new Date(application.observed.observed_at).toLocaleString()}
                  {health.status === 'stale' ? ' · Stale; refresh to verify current runs' : ''}
                </p>
              )}
              {observed?.job_runs?.length ? (
                <div className="overflow-x-auto">
                  <table className="data-table">
                    <thead>
                      <tr>
                        <th>Run</th>
                        <th>Revision</th>
                        <th>Status</th>
                        <th>Created</th>
                      </tr>
                    </thead>
                    <tbody>
                      {observed.job_runs.map((run) => (
                        <tr key={run.name}>
                          <td>
                            <code>{run.name}</code>
                          </td>
                          <td>r{run.revision}</td>
                          <td>
                            <Status value={run.status} />
                          </td>
                          <td>{new Date(run.created_at).toLocaleString()}</td>
                        </tr>
                      ))}
                    </tbody>
                  </table>
                </div>
              ) : (
                <p className="p-4 text-sm muted-text">
                  {!observed || ['unknown', 'stale', 'not observed'].includes(health.status)
                    ? 'Current run history is unavailable. Refresh the runtime observation.'
                    : 'No retained runs in this observation. Scheduled runs appear here after Kubernetes starts them; inspect their output in Logs.'}
                </p>
              )}
            </section>
          )}

          <>
            {service.actions && (
              <ManagedActionsStatus application={application} service={serviceName} />
            )}
          </>
          <div className="service-overview-grid">
            <section className="panel service-summary-panel">
              <div className="panel-heading">
                <h2>Runtime</h2>
                <span className="label-chip">{serviceProfileLabel(service)} profile</span>
              </div>
              {service.job?.schedule && (
                <p className="px-4 text-sm muted-text">
                  Overlapping runs are skipped. Timeout: {service.job.timeout_seconds || 300}s.
                  Retries: {service.job.retries || 0}.
                </p>
              )}
              <dl className="service-definition-list">
                <div>
                  <dt>Node placement</dt>
                  <dd className="break-all">{service.node_name || 'Automatic'}</dd>
                </div>
                {service.serverless && (
                  <div>
                    <dt>Serverless HTTP</dt>
                    <dd>
                      {service.serverless.min_replicas === 1
                        ? 'Always warm'
                        : `Sleep after ${service.serverless.idle_seconds || 300}s idle`}{' '}
                      · {service.serverless.max_concurrency || 16} concurrent requests
                    </dd>
                  </div>
                )}
                <div>
                  <dt>{service.job ? 'Job result' : 'Replicas'}</dt>
                  <dd>{runtimeReplicaSummary(health, Boolean(service.job))}</dd>
                </div>
                <div>
                  <dt>
                    {service.job?.schedule
                      ? 'Schedule'
                      : service.job
                        ? 'Completion gate'
                        : 'Readiness'}
                  </dt>
                  <dd>{readinessLabel(service)}</dd>
                </div>
                <div>
                  <dt>Configured exposure</dt>
                  <dd>
                    {service.public || Object.keys(service.http || {}).length > 0
                      ? service.public_tcp?.length
                        ? 'Public HTTP and TCP'
                        : 'Public HTTP'
                      : service.public_tcp?.length
                        ? 'Public TCP'
                        : hasPorts
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
                  <dd>{timestamp(health.observedAt)}</dd>
                </div>
              </dl>
              <ApplicationAlarmLinks application={application} />
            </section>
            <section
              className="panel service-summary-panel node-runtime service-runtime"
              aria-label="Service resource usage"
            >
              <div className="node-runtime-heading">
                <div className="node-runtime-title">
                  <h2>Resource usage</h2>
                  <Badge>{freshness}</Badge>
                </div>
                <div className="node-runtime-controls">
                  <Button
                    size="sm"
                    variant="ghost"
                    aria-pressed={paused}
                    onClick={() => setPaused((value) => !value)}
                  >
                    <Icon name={paused ? 'play' : 'pause'} size={14} />
                    {paused ? 'Resume live' : 'Pause live'}
                  </Button>
                  <Button
                    size="sm"
                    variant="ghost"
                    disabled={runtime.isFetching || !visible}
                    onClick={() => void runtime.refetch({ cancelRefetch: false })}
                  >
                    <Icon name="refresh" size={14} className={runtime.isFetching ? 'spin' : ''} />
                    {runtime.isFetching ? 'Probing…' : 'Probe now'}
                  </Button>
                </div>
              </div>
              {runtime.error && <RequestError error={message(runtime.error)} />}
              <div className="node-runtime-values">
                <ResourceMetric
                  label="CPU"
                  field="cpu"
                  used={available ? metrics.cpu_millicores : undefined}
                  samples={samples}
                />
                <ResourceMetric
                  label="Memory"
                  field="memory"
                  used={available ? metrics.memory_bytes : undefined}
                  samples={samples}
                />
              </div>
              <dl className="node-runtime-freshness">
                <div>
                  <dt>Source sample</dt>
                  <dd>
                    {metrics?.sampled_at ? (
                      <time dateTime={metrics.sampled_at}>
                        {sampleTimestamp(metrics.sampled_at)}
                      </time>
                    ) : (
                      'Unavailable'
                    )}
                    {sampleAge !== null && <span> · {Math.floor(sampleAge / 1000)}s old</span>}
                  </dd>
                </div>
                <div>
                  <dt>Last checked</dt>
                  <dd>
                    {runtime.data?.observed_at ? (
                      <time dateTime={runtime.data.observed_at}>
                        {sampleTimestamp(runtime.data.observed_at)}
                      </time>
                    ) : (
                      'Not checked'
                    )}
                  </dd>
                </div>
              </dl>
              <p className="node-runtime-help">
                Every 15s while visible · {samples.length} / {retainedMetricSamples} source samples.
                {metrics && (
                  <>
                    {' '}
                    {metrics.pods_sampled} / {metrics.pods_expected} pods sampled.
                  </>
                )}{' '}
                Usage totals the sampled pods; each chart scales to its recorded values.
              </p>
              {!available && (
                <Note>
                  {metrics?.reason || 'The cluster has not returned a current resource sample.'}
                </Note>
              )}
            </section>
          </div>
          <div className="section-toolbar">
            <div>
              <div className="hako-section-heading-title">
                <h2>Pods</h2>
                <HeadingHelp title="Pods">Live workloads owned by this service.</HeadingHelp>
              </div>
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
              <div className="hako-section-heading-title">
                <h2>Pods and events</h2>
                <HeadingHelp title="Pods and events">
                  Container state, allocations, readiness conditions, and Kubernetes events.
                </HeadingHelp>
              </div>
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
              features.terminal && scope.can('deployments:write')
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
        <Tabs.Content value="network" className="tab-content service-network-content">
          <section className="panel service-summary-panel">
            <div className="panel-heading">
              <h2>Service networking</h2>
              {scope.can('deployments:write') && (
                <Button size="sm" variant="ghost" onClick={() => edit('toml')}>
                  <Icon name="code" size={14} />
                  Edit configuration
                </Button>
              )}
            </div>
            <dl className="service-definition-list">
              <div>
                <dt>Configured exposure</dt>
                <dd>
                  {service.public || Object.keys(service.http || {}).length > 0
                    ? service.public_tcp?.length
                      ? 'Public HTTP and TCP'
                      : 'Public HTTP'
                    : service.public_tcp?.length
                      ? 'Public TCP'
                      : hasPorts
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
                  ) : service.ports?.length ? (
                    'Additional ports listed below'
                  ) : (
                    'No inbound port'
                  )}
                </dd>
              </div>
              <div>
                <dt>Public endpoints</dt>
                <dd>
                  <PublicEndpoints
                    endpoints={endpoints.filter((endpoint) => endpoint.service === serviceName)}
                  />
                </dd>
              </div>
              {service.backend_http2 && (
                <div>
                  <dt>Backend protocol</dt>
                  <dd>HTTP/2 (h2)</dd>
                </div>
              )}

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
                <dt>Allowed peers</dt>
                <dd className="service-peer-list">
                  {service.network_access === undefined
                    ? 'Services on shared networks'
                    : service.network_access.from.length
                      ? service.network_access.from.map((name) => (
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
                      : 'No peer services allowed'}
                </dd>
              </div>
              <div>
                <dt>Deployment dependencies</dt>
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
          {Boolean(service.ports?.length) && (
            <section className="service-private-ports">
              <div className="section-toolbar">
                <div>
                  <div className="hako-section-heading-title">
                    <h2>Additional private ports</h2>
                    <HeadingHelp title="Additional private ports">
                      Configured endpoints for permitted services on shared networks.
                    </HeadingHelp>
                  </div>
                </div>
              </div>
              <div className="table-container">
                <table>
                  <thead>
                    <tr>
                      <th>Name</th>
                      <th>Protocol</th>
                      <th>Private address</th>
                      <th>Container port</th>
                    </tr>
                  </thead>
                  <tbody>
                    {service.ports?.map((port) => (
                      <tr key={port.name}>
                        <td>{port.name}</td>
                        <td className="mono">{port.protocol}</td>
                        <td>
                          <span className="copyable-address">
                            <code>
                              {serviceName}:{port.port}
                            </code>
                            <Copy value={`${serviceName}:${port.port}`} />
                          </span>
                        </td>
                        <td className="mono">{port.target_port}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            </section>
          )}
          <Note>
            Private addresses are reachable by permitted workloads in this application. Browser
            requests use the public endpoint or a server-side proxy. Editing configuration opens the
            full application TOML so shared network and volume changes can be reviewed together.
          </Note>
          <Suspense fallback={<Loading rows={2} />}>
            <ServiceTLS application={application} service={serviceName} />
          </Suspense>
          <Suspense fallback={<Loading rows={2} />}>
            <ServiceDelivery application={application} serviceName={serviceName} />
          </Suspense>
        </Tabs.Content>
        <Tabs.Content value="environment" className="tab-content service-environment-tab">
          <div className="section-toolbar">
            <div>
              <div className="hako-section-heading-title">
                <h2>Environment variables</h2>
                <HeadingHelp title="Environment variables">
                  Application defaults and service overrides passed to this service’s containers.
                </HeadingHelp>
              </div>
            </div>
            {scope.can('deployments:write') && (
              <Button asChild variant="primary">
                <Link
                  to="/applications/$applicationId/environment"
                  params={{ applicationId: application.id }}
                  search={{ service: serviceName }}
                >
                  <Icon name="code" size={14} />
                  Edit variables
                </Link>
              </Button>
            )}
          </div>
          {Object.keys(effective.env || {}).length ? (
            <div className="table-container env-review-table">
              <table>
                <thead>
                  <tr>
                    <th>Variable</th>
                    <th>Value</th>
                  </tr>
                </thead>
                <tbody>
                  {Object.entries(effective.env || {})
                    .sort(([left], [right]) => left.localeCompare(right))
                    .map(([name, value]) => (
                      <tr key={name}>
                        <th scope="row">
                          <code className="break-text">{name}</code>
                          {!Object.hasOwn(service.env || {}, name) && (
                            <small className="block text-muted-foreground">From application</small>
                          )}
                        </th>
                        <td>
                          <pre>{value === '' ? '(empty string)' : value}</pre>
                        </td>
                      </tr>
                    ))}
                </tbody>
              </table>
            </div>
          ) : (
            <Empty
              icon="code"
              title="No plain variables"
              description="Add variables through a reviewed deployment for this service."
            />
          )}
          <p className="field-help">
            Plain values are stored in application revisions. Keep passwords and tokens in the
            Secrets tab.
          </p>
        </Tabs.Content>
        <Tabs.Content value="secrets" className="tab-content">
          <Suspense fallback={<Loading rows={2} />}>
            <ServiceSecrets application={application} serviceName={serviceName} />
          </Suspense>
        </Tabs.Content>
        <Tabs.Content value="settings" className="tab-content">
          {scope.can('deployments:write') && (
            <div className="section-toolbar">
              <p className="field-help">
                Remove this service through a reviewed application deployment. Review dependencies
                before deploying.
              </p>
              <Button variant="danger" size="sm" asChild>
                <Link
                  to="/applications/$applicationId/configure"
                  params={{ applicationId: application.id }}
                  search={{ remove: serviceName }}
                >
                  <Icon name="trash" size={14} />
                  Remove service
                </Link>
              </Button>
            </div>
          )}
          <div className="section-toolbar">
            <div>
              <div className="hako-section-heading-title">
                <h2>Applied service configuration</h2>
                <HeadingHelp title="Applied service configuration">
                  Changes create a reviewed, immutable revision for this application.
                </HeadingHelp>
              </div>
            </div>
            {scope.can('deployments:write') && (
              <Button variant="primary" onClick={() => edit('toml')}>
                <Icon name="code" size={14} />
                Edit configuration
              </Button>
            )}
          </div>
          <Note>
            Shared networks and volume declarations can affect other services. Configuration edits
            are reviewed for the whole application.
          </Note>
          {service.autoscaling && (
            <Note>Replica count is managed by this service's autoscaling configuration.</Note>
          )}
          <div className="service-settings-grid">
            <section className="panel service-summary-panel">
              <div className="panel-heading">
                <h2>Runtime settings</h2>
                <span className="label-chip">Revision {application.revision}</span>
              </div>
              <dl className="service-definition-list">
                <div>
                  <dt>User ID</dt>
                  <dd className="mono">{service.run_as_user || '10001 (default)'}</dd>
                </div>
                <div>
                  <dt>Group ID</dt>
                  <dd className="mono">
                    {service.run_as_group || `${service.run_as_user || 10001} (default)`}
                  </dd>
                </div>
                <div>
                  <dt>Volume group</dt>
                  <dd className="mono">
                    {service.fs_group || `${service.run_as_user || 10001} (default)`}
                  </dd>
                </div>
                <div>
                  <dt>Root filesystem</dt>
                  <dd>{service.read_only_root_filesystem ? 'Read-only' : 'Writable'}</dd>
                </div>
                <div>
                  <dt>Working directory</dt>
                  <dd className="mono break-text">{service.working_dir || 'Image default'}</dd>
                </div>
                <div>
                  <dt>Shutdown grace</dt>
                  <dd>
                    {service.termination_grace_seconds
                      ? `${service.termination_grace_seconds} seconds`
                      : '30 seconds (default)'}
                  </dd>
                </div>
              </dl>
              <p className="field-help">
                Configured values. Open a pod to inspect its observed state.
              </p>
            </section>
            <ServiceStorage
              application={application}
              serviceName={serviceName}
              service={service}
              volumes={application.spec.volumes}
            />
          </div>
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
                volumes: Object.fromEntries(
                  (service.mounts || []).flatMap((mount) => {
                    const volume = application.spec.volumes?.[mount.volume]
                    return volume ? [[mount.volume, volume]] : []
                  }),
                ),
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
          {error && <RequestError error={error} />}
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

function ServiceStorage({
  application,
  serviceName,
  service,
  volumes,
}: {
  application: Application
  serviceName: string
  service: Service
  volumes: Spec['volumes']
}) {
  const mounts = [
    ...(service.volume
      ? [
          {
            path: service.volume.mount_path,
            name: 'Service volume',
            claim: serviceName + '-data',
            size: service.volume.size_gib,
            details: `${service.volume.size_gib} GiB · ${service.volume.storage_class || 'Default storage class'}`,
            access: 'Read and write',
          },
        ]
      : []),
    ...(service.mounts || []).map((mount) => {
      const volume = volumes?.[mount.volume]
      return {
        path: mount.mount_path,
        name: mount.volume,
        claim: 'hakopod-volume-' + mount.volume,
        size: volume?.size_gib || 0,
        details: [
          volume
            ? `${volume.size_gib} GiB · ${volume.access_mode}`
            : 'Volume definition unavailable',
          volume?.storage_class || 'Default storage class',
          mount.sub_path ? `Subdirectory: ${mount.sub_path}` : '',
        ]
          .filter(Boolean)
          .join(' · '),
        access: mount.read_only ? 'Read-only' : 'Read and write',
      }
    }),
    ...(service.temporary_mounts || []).map((mount) => ({
      path: mount.mount_path,
      name: mount.memory ? 'Temporary memory' : 'Temporary disk',
      claim: '',
      size: 0,
      details: `${mount.size_mib} MiB limit · Removed with the pod`,
      access: 'Read and write',
    })),
  ]
  const seenClaims = new Set<string>()
  return (
    <section className="panel service-summary-panel">
      <div className="panel-heading">
        <h2>Storage mounts</h2>
        <span className="label-chip">{mounts.length} configured</span>
      </div>
      {mounts.length ? (
        <dl className="service-storage-list">
          {mounts.map((mount) => {
            const first = mount.claim && !seenClaims.has(mount.claim)
            seenClaims.add(mount.claim)
            return (
              <div key={mount.path}>
                <dt>
                  <code>{mount.path}</code>
                  <Copy value={mount.path} />
                </dt>
                <dd>
                  <strong>{mount.name}</strong>
                  <span>{mount.access}</span>
                  <small>{mount.details}</small>
                  {first && mount.size > 0 && (
                    <VolumeResizeButton
                      application={application}
                      claim={mount.claim}
                      size={mount.size}
                    />
                  )}
                </dd>
              </div>
            )
          })}
        </dl>
      ) : (
        <p className="field-help">
          No storage mounts configured. Container files are replaced with the pod.
        </p>
      )}
    </section>
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
