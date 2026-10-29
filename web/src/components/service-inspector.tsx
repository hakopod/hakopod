import { lazy, Suspense, type ReactNode } from 'react'
import { Link } from '@tanstack/react-router'
import { useQuery } from '@tanstack/react-query'
import * as Tabs from '@radix-ui/react-tabs'
import type { Application, Service } from '../lib/types'
import { client, unwrap } from '../lib/client'
import { relative, timestamp } from '../lib/api'
import { useScope } from '../lib/scope'
import { useEditionFeatures } from '../lib/dashboard-edition'
import { serviceRuntimeHealth } from '../lib/runtime-health'
import { serviceProfileLabel } from '../lib/service-resources'
import { InspectorSheet } from './inspector-sheet'
import { ObservedState } from './resource-card'
import { Copy, ErrorState, Loading, Status } from './shared'
import { Button } from './ui/button'
import { Logs } from './logs'
import { lastErrorLine } from '../lib/app-attention'

const PodTerminal = lazy(() => import('./pod-terminal'))

export type ServiceInspectorTab = 'overview' | 'logs' | 'shell' | 'config'

const readRuntime = (applicationId: string, service: string, signal: AbortSignal) =>
  unwrap(
    client.GET('/applications/{id}/services/{service}/runtime', {
      signal,
      params: { path: { id: applicationId, service } },
    }),
  )
type Runtime = Awaited<ReturnType<typeof readRuntime>>

function memory(bytes: number) {
  return bytes >= 1024 ** 3
    ? `${(bytes / 1024 ** 3).toFixed(2)} GiB`
    : `${(bytes / 1024 ** 2).toFixed(1)} MiB`
}

function serviceType(service: Service) {
  if (service.job) return 'Job'
  if (service.public_tcp?.length) return 'Public TCP'
  return service.public ? 'Public HTTP' : 'Private'
}

/** "registry/org/python:3.12@sha256:…" → "python:3.12", pinned when a digest is present. */
function shortImage(image: string) {
  const [ref, digest] = image.split('@')
  return { name: ref.split('/').pop() || ref, pinned: Boolean(digest) }
}

/**
 * The service inspector: a wide sheet over the application overview. It explains the
 * running service; edits link to their task pages. The caller owns the URL, so tab
 * changes and close are reported, never navigated here.
 */
export function ServiceInspector({
  application,
  serviceName,
  tab,
  onTabChange,
  onClose,
}: {
  application: Application
  serviceName: string
  tab: ServiceInspectorTab
  onTabChange: (tab: ServiceInspectorTab) => void
  onClose: () => void
}) {
  const scope = useScope()
  const features = useEditionFeatures()
  const service = application.spec.services[serviceName]
  const observed = application.observed?.services?.find((item) => item.name === serviceName)
  const health = serviceRuntimeHealth(observed, application.observed?.observed_at)
  const canLogs = scope.can('logs:read')
  const canShell = features.terminal
  const active = (tab === 'logs' && !canLogs) || (tab === 'shell' && !canShell) ? 'overview' : tab
  const image = service ? shortImage(service.image) : undefined
  const runtime = useQuery({
    queryKey: ['service-runtime', application.id, serviceName],
    queryFn: ({ signal }) => readRuntime(application.id, serviceName, signal),
    enabled: Boolean(service) && active === 'overview',
    refetchInterval: 10000,
    refetchIntervalInBackground: false,
    refetchOnWindowFocus: false,
    retry: false,
    gcTime: 0,
  })
  return (
    <InspectorSheet
      open
      onOpenChange={(open) => !open && onClose()}
      wide
      kind="service"
      footer={
        <div className="service-inspector-footer">
          <Button asChild variant="primary">
            <Link
              to="/applications/$applicationId"
              params={{ applicationId: application.id }}
              search={{ service: serviceName, tab: 'overview' }}
            >
              Open service page
            </Link>
          </Button>
          {scope.can('deployments:write') && service && (
            <>
              <Button asChild>
                <Link
                  to="/applications/$applicationId/environment"
                  params={{ applicationId: application.id }}
                  search={{ service: serviceName }}
                >
                  Environment variables
                </Link>
              </Button>
              <Button asChild variant="ghost">
                <Link
                  to="/applications/$applicationId/configure"
                  params={{ applicationId: application.id }}
                  search={{ mode: 'form' }}
                >
                  Edit configuration
                </Link>
              </Button>
            </>
          )}
        </div>
      }
      title={application.service_display_names?.[serviceName] || serviceName}
      state={
        <ObservedState status={health.status} observedAt={health.observedAt} note={health.note} />
      }
      description={
        service && image
          ? [
              serviceType(service),
              serviceProfileLabel(service),
              image.name,
              image.pinned && 'pinned',
            ]
              .filter(Boolean)
              .join(' · ')
          : 'Not in the current specification'
      }
    >
      <Tabs.Root
        value={active}
        onValueChange={(value) => onTabChange(value as ServiceInspectorTab)}
        className="service-inspector"
      >
        <Tabs.List className="tab-list" aria-label="Service inspector">
          <Tabs.Trigger className="tab-trigger" value="overview">
            Overview
          </Tabs.Trigger>
          {canLogs && (
            <Tabs.Trigger className="tab-trigger" value="logs">
              Logs
            </Tabs.Trigger>
          )}
          {canShell && (
            <Tabs.Trigger className="tab-trigger" value="shell">
              Shell
            </Tabs.Trigger>
          )}
          <Tabs.Trigger className="tab-trigger" value="config">
            Config
          </Tabs.Trigger>
        </Tabs.List>
        <Tabs.Content className="tab-content" value="overview">
          {runtime.isPending && runtime.fetchStatus !== 'idle' ? (
            <Loading rows={2} />
          ) : runtime.error ? (
            <ErrorState error={runtime.error} />
          ) : runtime.data ? (
            <RuntimeOverview
              application={application}
              serviceName={serviceName}
              runtime={runtime.data}
              canLogs={canLogs}
              onOpenLogs={() => onTabChange('logs')}
            />
          ) : null}
        </Tabs.Content>
        {canLogs && (
          <Tabs.Content className="tab-content" value="logs">
            {active === 'logs' && (
              <Logs
                applicationId={application.id}
                services={[serviceName]}
                initialService={serviceName}
              />
            )}
          </Tabs.Content>
        )}
        {canShell && (
          <Tabs.Content className="tab-content" value="shell">
            {active === 'shell' && (
              <Suspense fallback={<Loading rows={2} />}>
                <PodTerminal applicationId={application.id} services={[serviceName]} />
              </Suspense>
            )}
          </Tabs.Content>
        )}
        <Tabs.Content className="tab-content" value="config">
          {service && <ServiceConfig service={service} />}
        </Tabs.Content>
      </Tabs.Root>
    </InspectorSheet>
  )
}

function RuntimeOverview({
  application,
  serviceName,
  runtime,
  canLogs,
  onOpenLogs,
}: {
  application: Application
  serviceName: string
  runtime: Runtime
  canLogs: boolean
  onOpenLogs: () => void
}) {
  const service = application.spec.services[serviceName]
  const observed = application.observed?.services?.find((item) => item.name === serviceName)
  const metrics = runtime.metrics
  const window = metrics.window_seconds ? `${metrics.window_seconds}s window` : ''
  const restarts = runtime.pods.reduce(
    (total, pod) => total + pod.containers.reduce((n, c) => n + c.restarts, 0),
    0,
  )
  const nodes = [...new Set(runtime.pods.map((pod) => pod.node_name).filter(Boolean))]
  const latest = [...(application.deployments || [])].sort((a, b) => b.revision - a.revision)[0]
  const address =
    observed?.url && /^https?:\/\//.test(observed.url)
      ? observed.url
      : observed?.internal_address || (service?.port ? `${serviceName}:${service.port}` : '')
  const events = runtime.pods
    .flatMap((pod) => pod.events)
    .sort((a, b) => Date.parse(b.last_seen) - Date.parse(a.last_seen))
    .slice(0, 6)
  const facts: [string, ReactNode][] = [
    ['Replicas', observed ? `${observed.ready} / ${observed.desired} ready` : 'Not observed'],
    [
      'Endpoint',
      address ? (
        <span className="service-inspector-copy">
          <code title={address}>{address}</code>
          <Copy value={address} label={`Copy ${serviceName} address`} iconOnly />
        </span>
      ) : (
        'No endpoint'
      ),
    ],
    [
      'Image',
      service ? (
        <span title={service.image}>
          {shortImage(service.image).name}
          {shortImage(service.image).pinned && <small> · pinned</small>}
        </span>
      ) : (
        '—'
      ),
    ],
    [
      'Deployed',
      latest ? (
        <>
          Revision {application.revision} · {relative(latest.created_at)}
        </>
      ) : (
        'Not deployed'
      ),
    ],
    ['Node', nodes.length ? nodes.join(', ') : 'Not scheduled'],
    [
      'Restarts',
      restarts > 0 ? <span className="service-inspector-restarts">{restarts}</span> : 'None',
    ],
  ]
  return (
    <div className="service-inspector-overview">
      <dl className="service-inspector-facts">
        {facts.map(([label, value]) => (
          <div key={label}>
            <dt>{label}</dt>
            <dd>{value}</dd>
          </div>
        ))}
      </dl>
      <p className="service-inspector-usage">
        <span>
          CPU{' '}
          <strong className="mono">
            {metrics.available && metrics.cpu_millicores !== undefined
              ? `${Math.round(metrics.cpu_millicores)} mCPU`
              : 'Unavailable'}
          </strong>
        </span>
        <span>
          Memory{' '}
          <strong className="mono">
            {metrics.available && metrics.memory_bytes !== undefined
              ? memory(metrics.memory_bytes)
              : 'Unavailable'}
          </strong>
        </span>
        <span className="service-inspector-meta">
          {metrics.available ? window : metrics.reason}
        </span>
      </p>
      <section aria-labelledby="inspector-pods">
        <h3 id="inspector-pods">Pods</h3>
        {runtime.pods.length ? (
          <ul className="service-inspector-pods">
            {runtime.pods.map((pod) => {
              const podRestarts = pod.containers.reduce((n, c) => n + c.restarts, 0)
              const waiting = pod.containers.find((c) => c.reason === 'CrashLoopBackOff')
              return (
                <li key={pod.name}>
                  <div className="service-inspector-pod">
                    <span className="mono service-inspector-pod-name" title={pod.name}>
                      {pod.name}
                    </span>
                    <Status value={waiting ? 'CrashLoop' : pod.ready ? 'ready' : pod.phase} small />
                    {podRestarts > 0 && (
                      <span className="service-inspector-restarts">
                        {podRestarts} {podRestarts === 1 ? 'restart' : 'restarts'}
                      </span>
                    )}
                    <span className="service-inspector-meta">
                      {pod.node_name || 'Not assigned'} · {relative(pod.created_at)}
                    </span>
                  </div>
                  {waiting && (
                    <p className="service-inspector-waiting">
                      {waiting.name}: {waiting.reason}
                      {waiting.message && ` · ${waiting.message}`}
                    </p>
                  )}
                </li>
              )
            })}
          </ul>
        ) : (
          <p className="service-inspector-meta">No pods observed.</p>
        )}
        {runtime.truncated && (
          <p className="service-inspector-meta">Showing the first pods only.</p>
        )}
      </section>
      {canLogs && (
        <LogPreview applicationId={application.id} serviceName={serviceName} onOpen={onOpenLogs} />
      )}
      {events.length > 0 && (
        <section aria-labelledby="inspector-events">
          <h3 id="inspector-events">Recent events</h3>
          <ul className="service-inspector-events">
            {events.map((event, index) => (
              <li key={`${event.reason}-${event.last_seen}-${index}`}>
                <span className={event.type === 'Warning' ? 'service-inspector-restarts' : ''}>
                  {event.type} · {event.reason}
                  {event.count > 1 && ` ×${event.count}`}
                </span>
                <span>{event.message}</span>
                <time
                  className="service-inspector-meta"
                  dateTime={event.last_seen}
                  title={timestamp(event.last_seen)}
                >
                  {relative(event.last_seen)}
                </time>
              </li>
            ))}
          </ul>
        </section>
      )}
    </div>
  )
}

/** The last few log lines, error first, so the overview answers "what is it doing?". */
function LogPreview({
  applicationId,
  serviceName,
  onOpen,
}: {
  applicationId: string
  serviceName: string
  onOpen: () => void
}) {
  const logs = useQuery({
    queryKey: ['inspector-log-preview', applicationId, serviceName],
    queryFn: async ({ signal }) => {
      const result = await client.GET('/applications/{id}/logs', {
        signal,
        params: { path: { id: applicationId }, query: { service: serviceName, tail: 8 } },
        parseAs: 'text',
      })
      return typeof result.data === 'string' ? result.data : ''
    },
    refetchInterval: 30_000,
    refetchIntervalInBackground: false,
    retry: false,
  })
  const lines = (logs.data || '')
    .split('\n')
    .map((line) => line.replace(/^\d{4}-\d\d-\d\dT[\d:.]+Z\s+/, ''))
    .filter((line) => line.trim() && !/^\[[^\]]*\]$/.test(line.trim()))
    .slice(-6)
  // Only a line that reads like a failure is highlighted, never just the last line.
  const error = lastErrorLine(logs.data || '')
  const failing =
    /error|exception|fatal|panic|traceback|refused|denied|not set|failed|cannot|unable/i.test(error)
  return (
    <section aria-labelledby="inspector-logs">
      <div className="service-inspector-section-head">
        <h3 id="inspector-logs">Latest logs</h3>
        <Button size="sm" variant="ghost" onClick={onOpen}>
          Open logs
        </Button>
      </div>
      {logs.isPending ? (
        <Loading rows={1} />
      ) : lines.length ? (
        <pre className="service-inspector-log">
          {lines.map((line, index) => (
            <span
              key={index}
              className={failing && line.trim() === error ? 'service-inspector-log-error' : ''}
            >
              {line}
              {'\n'}
            </span>
          ))}
        </pre>
      ) : (
        <p className="service-inspector-meta">No recent log lines.</p>
      )}
    </section>
  )
}

function ServiceConfig({ service }: { service: Service }) {
  const resources = Object.entries(service.resources || {})
    .filter(([, value]) => value)
    .map(([key, value]) => `${key} ${value}`)
    .join(', ')
  const rows: [string, ReactNode][] = [
    ['Type', serviceType(service)],
    ['Image', <code className="inspector-image">{service.image}</code>],
    [
      'Size',
      resources ? `${serviceProfileLabel(service)} (${resources})` : serviceProfileLabel(service),
    ],
    ['Replicas', service.autoscaling ? 'Autoscaling' : String(service.replicas ?? 1)],
    [
      'Port',
      [service.port, ...(service.ports || []).map((port) => `${port.name} ${port.port}`)]
        .filter(Boolean)
        .join(', ') || 'None',
    ],
    ['Exposure', service.public ? 'Public' : 'Private'],
    ['Networks', service.networks?.join(', ') || 'Default'],
    ['Environment', `${Object.keys(service.env || {}).length} variables`],
    ['Healthcheck', service.healthcheck || 'None'],
  ]
  if (service.command?.length)
    rows.push(['Command', <code>{[...service.command, ...(service.args || [])].join(' ')}</code>])
  return (
    <>
      <dl className="service-inspector-config">
        {rows.map(([label, value]) => (
          <div key={label}>
            <dt>{label}</dt>
            <dd>{value}</dd>
          </div>
        ))}
      </dl>
    </>
  )
}
