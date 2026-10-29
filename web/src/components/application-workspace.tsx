import { useRef, useState } from 'react'
import { Link, useNavigate } from '@tanstack/react-router'
import { useQueries, useQuery } from '@tanstack/react-query'
import type { Application, DeploymentSummary } from '../lib/types'
import { APIError, message, relative } from '../lib/api'
import { client, unwrap } from '../lib/client'
import { useScope } from '../lib/scope'
import { serviceRuntimeHealth } from '../lib/runtime-health'
import {
  attentionRank,
  healthSentence,
  issuePhrase,
  lastErrorLine,
  needsAttention,
  specChanges,
} from '../lib/app-attention'
import { Button } from './ui/button'
import { Dialog } from './ui/dialog'
import { Icon } from './icons'
import { RequestError, Status } from './shared'
import { ResourceEmpty, ResourceRow, ResourceRows } from './resource-card'
import { ReviewFacts, ReviewNotice, reviewDialog } from './operation-review'

export type InspectTab = 'overview' | 'logs' | 'shell' | 'config'

/** Every service with its observed health, failing ones first. */
export function rankedServices(app: Application) {
  return Object.keys(app.spec.services)
    .map((name, index) => {
      const runtime = app.observed?.services?.find((s) => s.name === name)
      const health = serviceRuntimeHealth(runtime, app.observed?.observed_at)
      return { name, index, runtime, health, message: runtime?.message || health.note }
    })
    .sort(
      (a, b) =>
        attentionRank(a.health.status) - attentionRank(b.health.status) || a.index - b.index,
    )
}

const sortedDeployments = (app: Application) =>
  [...(app.deployments || [])].sort((a, b) => b.revision - a.revision)

/** One line that answers “is it OK?” before anything else. */
export function HealthLine({ app }: { app: Application }) {
  const services = rankedServices(app).map(({ name, health, message }) => ({
    name: app.service_display_names?.[name] || name,
    status: health.status,
    message,
  }))
  const latest = sortedDeployments(app)[0]
  const parts = healthSentence(services, latest)
  const failing = services.some((s) => needsAttention(s.status)) || latest?.status === 'failed'
  return (
    <p className={`app-health-line${failing ? ' app-health-line-failing' : ''}`} role="status">
      <span className="app-health-dot" aria-hidden="true" />
      {parts.map((part, index) => (
        <span key={part}>
          {index > 0 && <span aria-hidden="true"> · </span>}
          {part}
        </span>
      ))}
    </p>
  )
}

function useDeployment(id?: string) {
  return useQuery({
    queryKey: ['deployment', id],
    queryFn: ({ signal }) =>
      unwrap(client.GET('/deployments/{id}', { signal, params: { path: { id: id! } } })),
    enabled: Boolean(id),
    staleTime: 60_000,
  })
}

/** Joins the failing service, its last error and the release that preceded it. */
export function NeedsAttention({
  app,
  onInspect,
}: {
  app: Application
  onInspect: (service: string, tab: InspectTab) => void
}) {
  const scope = useScope()
  const failing = rankedServices(app).filter(({ health }) => needsAttention(health.status))
  const deployments = sortedDeployments(app)
  const latest = deployments[0]
  const latestFailed = latest?.status === 'failed'
  const lastGood = deployments.find((d) => d.status === 'succeeded' && d.revision < app.revision)
  const [rollback, setRollback] = useState<DeploymentSummary | null>(null)
  const first = failing[0]
  const logs = useQuery({
    queryKey: ['attention-log', app.id, first?.name],
    queryFn: async ({ signal }) => {
      const result = await client.GET('/applications/{id}/logs', {
        signal,
        params: { path: { id: app.id }, query: { service: first!.name, tail: 80 } },
        parseAs: 'text',
      })
      return typeof result.data === 'string' ? lastErrorLine(result.data) : ''
    },
    enabled: Boolean(first) && scope.can('logs:read'),
    refetchInterval: 15_000,
    refetchIntervalInBackground: false,
  })
  const current = useDeployment(latestFailed ? latest.id : undefined)
  const previous = useDeployment(
    latestFailed ? deployments.find((d) => d.revision < latest.revision)?.id : undefined,
  )
  if (!failing.length && !latestFailed) return null
  const changes =
    latestFailed && current.data ? specChanges(previous.data?.spec, current.data.spec) : undefined
  const label = (name: string) => app.service_display_names?.[name] || name
  const restarts = first?.runtime?.message?.match(/(\d+)\s+restarts?/i)?.[1]
  return (
    <section className="app-attention" aria-labelledby="app-attention-heading">
      <Icon name="alert" size={18} />
      <div className="app-attention-body">
        <h2 id="app-attention-heading">
          {first
            ? `${label(first.name)} is ${issuePhrase(first.health.status, first.message)}`
            : `Revision ${latest.revision} failed`}
          {first && latestFailed && ` since revision ${latest.revision}`}
          {failing.length > 1 && (
            <span className="app-attention-more">
              {' '}
              and {failing.length - 1} more {failing.length === 2 ? 'service' : 'services'}
            </span>
          )}
        </h2>
        {logs.data ? (
          <p className="app-attention-log">
            <span className="sr-only">Last error line: </span>
            <code>{logs.data}</code>
          </p>
        ) : first?.message ? (
          <p className="app-attention-detail">{first.message}</p>
        ) : null}
        {!first && current.data?.error && (
          <p className="app-attention-detail">{current.data.error}</p>
        )}
        {restarts && <p className="app-attention-detail">{restarts} restarts</p>}
        {changes && (
          <p className="app-attention-detail">
            {changes[0] === 'No configuration change'
              ? `Revision ${latest.revision} made no configuration change.`
              : `Revision ${latest.revision} changed: ${changes.join(', ')}`}
          </p>
        )}
        <div className="app-attention-actions">
          {first && scope.can('logs:read') && (
            <Button size="sm" onClick={() => onInspect(first.name, 'logs')}>
              View logs
            </Button>
          )}
          {first && (
            <Button size="sm" onClick={() => onInspect(first.name, 'config')}>
              Edit {label(first.name)}
            </Button>
          )}
          {latestFailed && (
            <Button size="sm" asChild>
              <Link to="/deployments/$deploymentId" params={{ deploymentId: latest.id }}>
                Inspect revision {latest.revision}
              </Link>
            </Button>
          )}
          {lastGood && scope.can('deployments:write') && (
            <Button size="sm" onClick={() => setRollback(lastGood)}>
              Review rollback to revision {lastGood.revision}
            </Button>
          )}
        </div>
      </div>
      {rollback && <RollbackReview app={app} target={rollback} onClose={() => setRollback(null)} />}
    </section>
  )
}

/** A rollback names its target and shows what will change before anything is sent. */
export function RollbackReview({
  app,
  target,
  onClose,
}: {
  app: Application
  target: DeploymentSummary
  onClose: () => void
}) {
  const navigate = useNavigate()
  const detail = useDeployment(target.id)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  // Keep one idempotency key per review so a lost response does not create a second rollback.
  const key = useRef(crypto.randomUUID())
  const changes = detail.data ? specChanges(app.spec, detail.data.spec) : undefined
  async function submit() {
    setBusy(true)
    setError('')
    try {
      const accepted = await unwrap(
        client.POST('/applications/{id}/rollback', {
          params: { path: { id: app.id }, header: { 'Idempotency-Key': key.current } },
          body: { revision: target.revision, expected_revision: app.revision },
        }),
      )
      void navigate({ to: '/deployments/$deploymentId', params: { deploymentId: accepted.id } })
    } catch (err) {
      if (err instanceof APIError && [400, 403, 404, 409, 422].includes(err.status))
        key.current = crypto.randomUUID()
      setError(message(err))
      setBusy(false)
    }
  }
  return (
    <Dialog
      open
      className={reviewDialog}
      onOpenChange={(open) => !open && !busy && onClose()}
      title={`Roll back ${app.display_name || app.name} to revision ${target.revision}?`}
      description="A rollback creates a new revision from the recorded images and configuration."
    >
      <div className="dialog-body">
        <ReviewFacts
          facts={[
            ['Scope', `${app.project} / ${app.environment}`],
            ['From', `Revision ${app.revision}`],
            ['To', `Revision ${target.revision} · deployed ${relative(target.created_at)}`],
            ['Changes', changes ? changes.join(', ') : 'Loading the recorded configuration…'],
          ]}
        />
        <ReviewNotice tone="warn" title="Data is not rolled back">
          Database migrations and external secret values keep their current state.
        </ReviewNotice>
        {error && <RequestError error={error} />}
      </div>
      <div className="dialog-footer">
        <Button disabled={busy} onClick={onClose}>
          Cancel
        </Button>
        <Button
          variant="primary"
          aria-busy={busy || undefined}
          disabled={busy || !detail.data}
          onClick={() => void submit()}
        >
          {busy ? 'Rolling back…' : `Roll back to revision ${target.revision}`}
        </Button>
      </div>
    </Dialog>
  )
}

const duration = (start?: string | null, end?: string | null) => {
  if (!start || !end) return ''
  const seconds = Math.max(0, Math.round((Date.parse(end) - Date.parse(start)) / 1000))
  return seconds < 60 ? `${seconds}s` : `${Math.floor(seconds / 60)}m ${seconds % 60}s`
}

/** What changed, when, and how it went: deployments with their change summary. */
export function ActivityList({ app, limit }: { app: Application; limit?: number }) {
  const scope = useScope()
  const deployments = sortedDeployments(app).slice(0, limit ?? 20)
  // Each summary compares a revision with the one before it, so fetch one extra.
  const ids = [...deployments, sortedDeployments(app)[deployments.length]].filter(Boolean)
  const details = useQueries({
    queries: ids.map((d) => ({
      queryKey: ['deployment', d!.id],
      queryFn: ({ signal }: { signal: AbortSignal }) =>
        unwrap(client.GET('/deployments/{id}', { signal, params: { path: { id: d!.id } } })),
      staleTime: 60_000,
    })),
  })
  const byRevision = new Map(details.flatMap((q) => (q.data ? [[q.data.revision, q.data]] : [])))
  const [rollback, setRollback] = useState<DeploymentSummary | null>(null)
  if (!deployments.length)
    return (
      <ResourceEmpty
        icon="branch"
        title="No deployments recorded"
        description="Releases appear here with what they changed and how they went."
      />
    )
  return (
    <>
      <ResourceRows label="Activity, most recent first" className="app-activity">
        {deployments.map((deployment) => {
          const detail = byRevision.get(deployment.revision)
          const before = [...byRevision.values()]
            .filter((d) => d.revision < deployment.revision)
            .sort((a, b) => b.revision - a.revision)[0]
          const changes = detail ? specChanges(before?.spec, detail.spec) : undefined
          return (
            <ResourceRow
              key={deployment.id}
              identity={
                <Link
                  className="resource-row-link"
                  to="/deployments/$deploymentId"
                  params={{ deploymentId: deployment.id }}
                >
                  Revision {deployment.revision}
                  {deployment.revision === app.revision && (
                    <span className="current-label">Current</span>
                  )}
                </Link>
              }
              detail={
                <span className="app-activity-detail">
                  {changes ? changes.join(', ') : ' '}
                  {detail?.status === 'failed' && detail.error && (
                    <span className="app-activity-error">{detail.error}</span>
                  )}
                </span>
              }
              status={<Status value={deployment.status} small />}
              meta={
                <span className="app-activity-meta">
                  <time title={deployment.created_at} dateTime={deployment.created_at}>
                    {relative(deployment.created_at)}
                  </time>
                  {detail && duration(detail.started_at, detail.finished_at) && (
                    <span> · {duration(detail.started_at, detail.finished_at)}</span>
                  )}
                </span>
              }
              actions={
                scope.can('deployments:write') &&
                deployment.status === 'succeeded' &&
                deployment.revision < app.revision ? (
                  <Button size="sm" onClick={() => setRollback(deployment)}>
                    Roll back
                  </Button>
                ) : undefined
              }
            />
          )
        })}
      </ResourceRows>
      {rollback && <RollbackReview app={app} target={rollback} onClose={() => setRollback(null)} />}
    </>
  )
}
