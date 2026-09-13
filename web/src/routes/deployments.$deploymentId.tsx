import { useEffect, useState } from 'react'
import * as Tabs from '@radix-ui/react-tabs'
import { Brackets } from '@hakopod/hatch-ui/components/brackets'
import { Pipeline, type PipelineStage } from '@hakopod/hatch-ui/blocks/pipeline'
import { createFileRoute, Link } from '@tanstack/react-router'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import type { Plan } from '../lib/types'
import { activeDeployment, message, relative, timestamp } from '../lib/api'
import { client, unwrap } from '../lib/client'
import { useScope } from '../lib/scope'
import { currentDeploymentRuntime } from '../lib/runtime-health'
import { RuntimeNotice } from '../components/runtime-notice'
import { Icon } from '../components/icons'
import { Button } from '../components/ui/button'
import { Dialog } from '../components/ui/dialog'
import { HeadingHelp, Copy, ErrorState, Loading, Note, Status } from '../components/shared'
import { DiffTable } from '../components/deploy-dialog'

export const Route = createFileRoute('/deployments/$deploymentId')({ component: DeploymentDetail })
function DeploymentDetail() {
  const { deploymentId } = Route.useParams()
  const scope = useScope()
  const queryClient = useQueryClient()
  const [stage, setStage] = useState('reconcile')
  const [tab, setTab] = useState('events')
  const [cancelOpen, setCancelOpen] = useState(false)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const deployment = useQuery({
    queryKey: ['deployment', deploymentId],
    queryFn: ({ signal }) =>
      unwrap(client.GET('/deployments/{id}', { signal, params: { path: { id: deploymentId } } })),
    refetchInterval: (query) => (activeDeployment(query.state.data?.status) ? 2500 : false),
    gcTime: 0,
    refetchIntervalInBackground: false,
  })
  const application = useQuery({
    queryKey: ['application', deployment.data?.application_id],
    queryFn: ({ signal }) =>
      unwrap(
        client.GET('/applications/{id}', {
          signal,
          params: { path: { id: deployment.data!.application_id } },
        }),
      ),
    enabled: Boolean(deployment.data?.application_id),
    gcTime: 0,
  })
  const previousSummary = application.data?.deployments?.find(
    (item) => item.revision === (deployment.data?.revision || 0) - 1,
  )
  const previousDeployment = useQuery({
    queryKey: ['deployment', previousSummary?.id],
    queryFn: ({ signal }) =>
      unwrap(
        client.GET('/deployments/{id}', { signal, params: { path: { id: previousSummary!.id } } }),
      ),
    enabled: Boolean(previousSummary?.id),
    staleTime: Infinity,
    gcTime: 0,
  })
  useEffect(() => {
    if (application.data) scope.syncScope(application.data.project, application.data.environment)
  }, [application.data?.project, application.data?.environment, scope.syncScope])
  if (deployment.isPending) return <Loading rows={4} />
  if (deployment.error || !deployment.data)
    return <ErrorState error={deployment.error} retry={() => void deployment.refetch()} />
  const release = deployment.data
  const runtimeHealth = currentDeploymentRuntime(
    application.error ? undefined : application.data,
    release.revision,
  )
  const previous = previousDeployment.data
  const recentRevisions = [...(application.data?.deployments || [])]
  if (!recentRevisions.some((run) => run.id === release.id)) recentRevisions.push(release)
  recentRevisions.sort((a, b) => b.revision - a.revision)
  const changedFields: Plan['changes'] = []
  if (previous || release.revision === 1) {
    const serviceNames = new Set([
      ...Object.keys(previous?.spec?.services || {}),
      ...Object.keys(release.spec?.services || {}),
    ])
    for (const service of serviceNames) {
      const before = previous?.spec?.services[service]
      const after = release.spec?.services[service]
      for (const field of new Set([...Object.keys(before || {}), ...Object.keys(after || {})])) {
        const a = before?.[field as keyof typeof before]
        const b = after?.[field as keyof typeof after]
        if (JSON.stringify(a) !== JSON.stringify(b))
          changedFields.push({
            service,
            field,
            before: a,
            after: b,
            sensitive: field === 'secrets' || field === 'env',
          })
      }
    }
    if (JSON.stringify(previous?.spec?.networks) !== JSON.stringify(release.spec?.networks))
      changedFields.push({
        service: '',
        field: 'networks',
        before: previous?.spec?.networks,
        after: release.spec?.networks,
        sensitive: false,
      })
  }
  const results = release.result?.services || []
  const elapsed =
    release.started_at && release.finished_at
      ? Math.max(
          0,
          Math.round(
            (new Date(release.finished_at).getTime() - new Date(release.started_at).getTime()) /
              1000,
          ),
        )
      : null
  const imageResolved =
    Boolean(release.resolved_spec) ||
    (release.events || []).some((event) => event.type === 'resolved')
  const stages: PipelineStage[] = [
    {
      id: 'accepted',
      label: 'Accepted',
      state: 'success',
      description: `Revision ${release.revision} persisted`,
    },
    {
      id: 'images',
      label: 'Images',
      state: imageResolved ? 'success' : 'neutral',
      description: imageResolved ? 'Digests resolved' : 'No resolution recorded',
    },
    {
      id: 'reconcile',
      label: 'Reconcile',
      state:
        release.status === 'running'
          ? 'running'
          : release.status === 'succeeded'
            ? 'success'
            : release.status === 'failed'
              ? 'error'
              : 'neutral',
      description: release.status,
    },
    {
      id: 'result',
      label: 'Result',
      state:
        release.status === 'succeeded'
          ? 'success'
          : release.status === 'failed'
            ? 'error'
            : 'neutral',
      description: release.finished_at ? release.status : 'Not finished',
    },
  ]
  const stageContext: Record<string, string> = {
    accepted: `Accepted ${timestamp(release.created_at)}. This immutable revision contains ${Object.keys(release.spec.services).length} services.`,
    images: imageResolved
      ? 'The recorded resolved specification pins container images to digests. Inspect Services to copy the exact images.'
      : 'No resolved specification has been recorded for this revision yet.',
    reconcile: release.started_at
      ? `Started ${timestamp(release.started_at)}. Events below record workload changes, readiness, and any recovery.`
      : 'This deployment has not recorded a start time.',
    result: release.finished_at
      ? `Finished ${timestamp(release.finished_at)}${elapsed !== null ? ` after ${elapsed}s` : ''}. The recorded result is ${release.status}.`
      : 'A final result has not been recorded yet.',
  }
  return (
    <div className="ops-page ops-deployment-page">
      <div className="application-heading">
        <div>
          <div className="title-row hako-page-heading-title">
            <h1>
              Deployment <span className="muted-text">r{release.revision}</span>
            </h1>
          </div>
          <div className="application-metadata">
            <code>{release.id}</code>
            <Copy value={release.id} />
            <span>Deployment outcome</span>
            <Status value={release.status} small />
            <time dateTime={release.created_at} title={release.created_at}>
              {relative(release.created_at)}
            </time>
          </div>
        </div>
        <span className="form-spacer" />
        {activeDeployment(release.status) && scope.can('deployments:write') && (
          <Button onClick={() => setCancelOpen(true)}>
            <Icon name="x" size={15} />
            Cancel deployment
          </Button>
        )}
        <Button
          size="icon"
          variant="ghost"
          aria-label="Refresh deployment"
          onClick={() => {
            void deployment.refetch()
            void application.refetch()
          }}
        >
          <Icon name="refresh" size={16} className={deployment.isFetching ? 'spin' : ''} />
        </Button>
      </div>
      {runtimeHealth && (
        <>
          <div className="application-metadata runtime-summary">
            <span>Last observed runtime</span>
            <Status value={runtimeHealth.status} small />
            {runtimeHealth.observedAt && (
              <time dateTime={runtimeHealth.observedAt}>
                Observed {timestamp(runtimeHealth.observedAt)}
              </time>
            )}
            <Button size="sm" variant="ghost" asChild>
              <Link
                to="/applications/$applicationId"
                params={{ applicationId: release.application_id }}
              >
                Inspect application
              </Link>
            </Button>
          </div>
          <RuntimeNotice
            health={runtimeHealth}
            applicationId={release.application_id}
            canInspectNodes={scope.can('admin')}
            canInspectLogs={scope.can('logs:read')}
          />
        </>
      )}
      {(application.error || !application.data) && (
        <Note>
          Current application runtime is unavailable. The deployment outcome is a recorded result.
        </Note>
      )}
      {release.error && (
        <div className="deployment-failure" role="alert">
          <Icon name="alert" size={21} />
          <div>
            <strong>Deployment needs attention</strong>
            <p>{release.error}</p>
          </div>
        </div>
      )}
      {release.cancel_requested && (
        <Note>Cancellation requested. Waiting for the reconciler to reach a safe boundary.</Note>
      )}
      <div className="ops-run-layout">
        <aside className="ops-run-list" aria-label="Deployment history">
          <div className="ops-list-heading">Recorded deployment outcomes</div>
          {recentRevisions.map((run) => (
            <Link
              key={run.id}
              to="/deployments/$deploymentId"
              params={{ deploymentId: run.id }}
              className={`ops-run-card interactive ${run.id === release.id && activeDeployment(run.status) ? 'hatch' : ''}`}
              data-selected={run.id === release.id}
              aria-current={run.id === release.id ? 'page' : undefined}
            >
              <Brackets />
              <div className="ops-object">
                <Status value={run.status} small />
                <span className="mono">r{run.revision}</span>
              </div>
              <code>{run.id.slice(0, 12)}</code>
              <time dateTime={run.created_at} title={run.created_at}>
                {relative(run.created_at)}
              </time>
            </Link>
          ))}
          {application.error && <p className="field-help">Application history is unavailable.</p>}
        </aside>
        <div className="ops-run-detail">
          <Pipeline stages={stages} active={stage} onStageChange={setStage} />
          <div className="ops-stage-context" role="status">
            {stageContext[stage]}
          </div>
          <Tabs.Root value={tab} onValueChange={setTab}>
            <Tabs.List className="tab-list" aria-label="Deployment inspection">
              <Tabs.Trigger className="tab-trigger" value="events">
                Events
              </Tabs.Trigger>
              <Tabs.Trigger className="tab-trigger" value="changes">
                Changes
              </Tabs.Trigger>
              <Tabs.Trigger className="tab-trigger" value="services">
                Services
              </Tabs.Trigger>
            </Tabs.List>
            <Tabs.Content className="tab-content" value="events">
              <div className="ops-event-toolbar">
                <p>
                  {activeDeployment(release.status)
                    ? 'Observing the reconciler every 2.5 seconds.'
                    : 'Events retained with this release.'}
                </p>
                <Copy
                  value={(release.events || [])
                    .slice(-200)
                    .map(
                      (event) =>
                        `${event.time} ${event.type} ${event.service || ''} ${event.message}`,
                    )
                    .join('\n')}
                  label="Copy events"
                />
              </div>
              <div
                className="ops-deployment-events"
                role="log"
                aria-label="Recorded deployment events"
                aria-live="off"
              >
                <div className="ops-deployment-event">
                  <time dateTime={release.created_at} title={release.created_at}>
                    {timestamp(release.created_at)}
                  </time>
                  <span className="ops-event-type">
                    <Icon name="check" size={14} />
                    accepted
                  </span>
                  <span>Revision {release.revision} persisted by the management API.</span>
                </div>
                {(release.events || []).slice(-200).map((event, index) => (
                  <div
                    className={`ops-deployment-event ${/fail|error/.test(event.type) ? 'ops-event-error' : ''}`}
                    key={event.id || index}
                  >
                    <time dateTime={event.time} title={event.time}>
                      {timestamp(event.time)}
                    </time>
                    <span className="ops-event-type">
                      <Icon
                        name={
                          /fail|error/.test(event.type)
                            ? 'alert'
                            : /success|ready|complete/.test(event.type)
                              ? 'check'
                              : 'activity'
                        }
                        size={14}
                      />
                      {event.type.replaceAll('_', ' ')}
                    </span>
                    <span>
                      {event.service && <code className="ops-event-service">{event.service}</code>}
                      {event.message}
                    </span>
                  </div>
                ))}
                {!(release.events || []).length && (
                  <p className="ops-event-empty">
                    {activeDeployment(release.status)
                      ? 'Waiting for the reconciler’s first event.'
                      : 'No additional events were recorded.'}
                  </p>
                )}
              </div>
            </Tabs.Content>
            <Tabs.Content className="tab-content" value="changes">
              <div className="section-toolbar">
                <div>
                  <h2>Configuration diff</h2>
                  <p>
                    {release.revision === 1
                      ? 'Initial release'
                      : `r${release.revision - 1} → r${release.revision}`}
                  </p>
                </div>
              </div>
              {previousDeployment.isFetching && !previous ? (
                <Loading rows={1} />
              ) : previousDeployment.error ? (
                <ErrorState
                  error={previousDeployment.error}
                  retry={() => void previousDeployment.refetch()}
                />
              ) : previous || release.revision === 1 ? (
                <DiffTable changes={changedFields} />
              ) : (
                <Note>
                  The previous revision is outside the loaded history. Open the application’s
                  configuration to inspect its current specification.
                </Note>
              )}
            </Tabs.Content>
            <Tabs.Content className="tab-content" value="services">
              <div className="section-toolbar">
                <div>
                  <div className="hako-section-heading-title">
                    <h2>Recorded service results</h2>
                    <HeadingHelp title="Recorded service results">
                      Observations saved with this deployment. Open a service to inspect its current
                      runtime health.
                    </HeadingHelp>
                  </div>
                </div>
              </div>
              <div className="table-container ops-table">
                <table>
                  <thead>
                    <tr>
                      <th>Service / recorded state</th>
                      <th>Image</th>
                      <th>Recorded replicas</th>
                    </tr>
                  </thead>
                  <tbody>
                    {Object.entries(release.spec.services).map(([name, service]) => {
                      const result = results.find((item) => item.name === name)
                      const resolved = release.resolved_spec?.services?.[name]?.image
                      return (
                        <tr key={name}>
                          <td>
                            <div className="ops-object">
                              <Status value={result?.status || 'not observed'} small />
                              <Link
                                className="ops-object-name"
                                to="/applications/$applicationId"
                                params={{ applicationId: release.application_id }}
                                search={{ service: name }}
                              >
                                {name}
                              </Link>
                            </div>
                            {result?.message && (
                              <small className="ops-table-sub">{result.message}</small>
                            )}
                          </td>
                          <td>
                            <code className="ops-image" title={resolved || service.image}>
                              {resolved || service.image}
                            </code>
                            <div className="ops-object-id">
                              <small>{resolved ? 'Resolved image' : 'Requested image'}</small>
                              <Copy value={resolved || service.image} label="Copy image" />
                            </div>
                          </td>
                          <td className="mono">
                            {result ? `${result.ready} / ${result.desired} ready` : 'Not observed'}
                          </td>
                        </tr>
                      )
                    })}
                  </tbody>
                </table>
              </div>
            </Tabs.Content>
          </Tabs.Root>
        </div>
      </div>
      <Dialog
        open={cancelOpen}
        onOpenChange={(open) => {
          if (!busy) setCancelOpen(open)
        }}
        title="Cancel this deployment?"
        description="Request cancellation at the reconciler’s next safe boundary."
      >
        <div className="dialog-body">
          <Note>
            Resources already applied may remain. Cancellation does not tear down workloads or imply
            a rollback. The recorded deployment result reports what happened.
          </Note>
          {error && <div className="inline-error">{error}</div>}
        </div>
        <div className="dialog-footer">
          <Button disabled={busy} onClick={() => setCancelOpen(false)}>
            Keep deploying
          </Button>
          <Button
            variant="danger"
            disabled={busy}
            onClick={async () => {
              setBusy(true)
              setError('')
              try {
                await unwrap(
                  client.POST('/deployments/{id}/cancel', {
                    params: { path: { id: release.id } },
                    body: {},
                  }),
                )
                setCancelOpen(false)
                void queryClient.invalidateQueries({ queryKey: ['deployment', release.id] })
              } catch (err) {
                setError(message(err))
              } finally {
                setBusy(false)
              }
            }}
          >
            {busy ? 'Requesting…' : 'Cancel deployment'}
          </Button>
        </div>
      </Dialog>
    </div>
  )
}
