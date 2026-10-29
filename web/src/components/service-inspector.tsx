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
import { ErrorState, Loading, Status } from './shared'
import { Button } from './ui/button'
import { Logs } from './logs'

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
            <RuntimeOverview runtime={runtime.data} />
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
          {service && (
            <ServiceConfig
              applicationId={application.id}
              serviceName={serviceName}
              service={service}
              canEdit={scope.can('deployments:write')}
            />
          )}
        </Tabs.Content>
      </Tabs.Root>
    </InspectorSheet>
  )
}

function RuntimeOverview({ runtime }: { runtime: Runtime }) {
  const metrics = runtime.metrics
  const window = metrics.window_seconds ? `${metrics.window_seconds}s window` : undefined
  const events = runtime.pods
    .flatMap((pod) => pod.events)
    .sort((a, b) => Date.parse(b.last_seen) - Date.parse(a.last_seen))
    .slice(0, 10)
  return (
    <>
      <dl className="service-inspector-metrics">
        <Metric
          label="CPU"
          value={
            metrics.available && metrics.cpu_millicores !== undefined
              ? `${metrics.cpu_millicores.toFixed(1)} mCPU`
              : undefined
          }
          window={window}
          reason={metrics.reason}
        />
        <Metric
          label="Memory"
          value={
            metrics.available && metrics.memory_bytes !== undefined
              ? memory(metrics.memory_bytes)
              : undefined
          }
          window={window}
          reason={metrics.reason}
        />
      </dl>
      <h3>Pods</h3>
      {runtime.pods.length ? (
        <ul className="service-inspector-pods">
          {runtime.pods.map((pod) => {
            const restarts = pod.containers.reduce((n, container) => n + container.restarts, 0)
            const waiting = pod.containers.find(
              (container) => container.reason === 'CrashLoopBackOff',
            )
            return (
              <li key={pod.name}>
                <div className="service-inspector-pod">
                  <span className="mono service-inspector-pod-name" title={pod.name}>
                    {pod.name}
                  </span>
                  <Status value={waiting ? 'CrashLoop' : pod.ready ? 'ready' : pod.phase} small />
                  {restarts > 0 && (
                    <span className="service-inspector-restarts">
                      {restarts} {restarts === 1 ? 'restart' : 'restarts'}
                    </span>
                  )}
                  <span className="mono service-inspector-meta" title="Node">
                    {pod.node_name || 'Not assigned'}
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
      {runtime.truncated && <p className="service-inspector-meta">Showing the first pods only.</p>}
      {events.length > 0 && (
        <>
          <h3>Recent events</h3>
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
        </>
      )}
    </>
  )
}

function Metric({
  label,
  value,
  window,
  reason,
}: {
  label: string
  value?: string
  window?: string
  reason?: string
}) {
  return (
    <div>
      <dt>{label}</dt>
      <dd>
        <strong className="mono">{value ?? 'Unavailable'}</strong>
        <span className="service-inspector-meta">{value ? window : reason}</span>
      </dd>
    </div>
  )
}

function ServiceConfig({
  applicationId,
  serviceName,
  service,
  canEdit,
}: {
  applicationId: string
  serviceName: string
  service: Service
  canEdit: boolean
}) {
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
      <div className="service-inspector-actions">
        {canEdit && (
          <>
            <Button asChild variant="outline">
              <Link
                to="/applications/$applicationId/environment"
                params={{ applicationId }}
                search={{ service: serviceName }}
              >
                Edit environment variables
              </Link>
            </Button>
            <Button asChild variant="outline">
              <Link
                to="/applications/$applicationId/configure"
                params={{ applicationId }}
                search={{ mode: 'form' }}
              >
                Edit service configuration
              </Link>
            </Button>
          </>
        )}
        <Button asChild variant="ghost">
          <Link
            to="/applications/$applicationId"
            params={{ applicationId }}
            search={{ service: serviceName, tab: 'overview' }}
          >
            Open full service page
          </Link>
        </Button>
      </div>
    </>
  )
}
