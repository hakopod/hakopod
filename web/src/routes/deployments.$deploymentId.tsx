import { useEffect, useState } from 'react'
import { createFileRoute, Link } from '@tanstack/react-router'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import type { Plan } from '../lib/types'
import { activeDeployment, message, timestamp } from '../lib/api'
import { client, unwrap } from '../lib/client'
import { useScope } from '../lib/scope'
import { Icon } from '../components/icons'
import { Button } from '../components/ui/button'
import { Dialog } from '../components/ui/dialog'
import { Copy, ErrorState, Loading, Note, Status } from '../components/shared'
import { DiffTable } from '../components/deploy-dialog'

export const Route = createFileRoute('/deployments/$deploymentId')({ component: DeploymentDetail })
function DeploymentDetail() {
  const { deploymentId } = Route.useParams()
  const scope = useScope()
  const queryClient = useQueryClient()
  const [cancelOpen, setCancelOpen] = useState(false)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const deployment = useQuery({
    queryKey: ['deployment', deploymentId],
    queryFn: ({ signal }) =>
      unwrap(client.GET('/deployments/{id}', { signal, params: { path: { id: deploymentId } } })),
    refetchInterval: (query) => (activeDeployment(query.state.data?.status) ? 2500 : false),
    gcTime: 0,
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
  const previous = previousDeployment.data
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
  return (
    <>
      <Link
        to="/applications/$applicationId"
        params={{ applicationId: release.application_id }}
        className="back-link"
      >
        <Icon name="back" size={14} />
        {application.data?.name || 'Application'}
      </Link>
      <div className="application-heading">
        <div className="app-symbol app-symbol-large">
          <Icon name="branch" size={27} />
        </div>
        <div>
          <div className="title-row">
            <h1>
              Deployment <span className="muted-text">r{release.revision}</span>
            </h1>
            <Status value={release.status} />
          </div>
          <div className="application-metadata">
            <code>{release.id}</code>
            <Copy value={release.id} />
            <span>{timestamp(release.created_at)}</span>
          </div>
        </div>
        <div className="form-spacer" />
        {activeDeployment(release.status) && scope.can('deployments:write') && (
          <Button onClick={() => setCancelOpen(true)}>
            <Icon name="x" size={15} />
            Cancel deployment
          </Button>
        )}
        <Button
          size="icon"
          aria-label="Refresh deployment"
          onClick={() => {
            void deployment.refetch()
            void application.refetch()
          }}
        >
          <Icon name="refresh" size={16} className={deployment.isFetching ? 'spin' : ''} />
        </Button>
      </div>
      {release.error && (
        <div className="deployment-failure" role="alert">
          <Icon name="alert" size={21} />
          <div>
            <strong>Deployment needs attention</strong>
            <p>{release.error}</p>
          </div>
        </div>
      )}
      <div className="deployment-summary">
        <div>
          <span>APPLICATION</span>
          <Link
            to="/applications/$applicationId"
            params={{ applicationId: release.application_id }}
          >
            {application.data?.name || release.spec?.name}
            <Icon name="chevron" size={13} />
          </Link>
        </div>
        <div>
          <span>STARTED</span>
          <strong>{release.started_at ? timestamp(release.started_at) : 'Waiting in queue'}</strong>
        </div>
        <div>
          <span>DURATION</span>
          <strong>
            {elapsed !== null
              ? `${elapsed}s`
              : activeDeployment(release.status)
                ? 'In progress'
                : '—'}
          </strong>
        </div>
        <div>
          <span>SERVICES</span>
          <strong>{Object.keys(release.spec?.services || {}).length} in this revision</strong>
        </div>
      </div>
      <div className="deployment-columns">
        <div>
          <section className="panel">
            <div className="panel-heading">
              <h2>Deployment timeline</h2>
              <span className="label-chip">
                <Icon name="activity" size={12} />
                {activeDeployment(release.status) ? 'Auto-refreshing' : 'Recorded events'}
              </span>
            </div>
            <div className="timeline">
              <div className="timeline-event">
                <span className="timeline-icon">
                  <Icon name="check" size={13} />
                </span>
                <div>
                  <h3>Deployment accepted</h3>
                  <p>Revision {release.revision} was persisted by the management API.</p>
                  <time>{timestamp(release.created_at)}</time>
                </div>
              </div>
              {(release.events || []).slice(-200).map((event, index) => (
                <div
                  className={`timeline-event ${/fail|error/.test(event.type) ? 'timeline-error' : ''}`}
                  key={event.id || index}
                >
                  <span className="timeline-icon">
                    <Icon
                      name={
                        /fail|error/.test(event.type)
                          ? 'x'
                          : /success|ready|complete/.test(event.type)
                            ? 'check'
                            : 'activity'
                      }
                      size={13}
                    />
                  </span>
                  <div>
                    <h3>
                      {event.service && <span className="event-service">{event.service}</span>}
                      {event.type.replaceAll('_', ' ')}
                    </h3>
                    <p>{event.message}</p>
                    <time>{timestamp(event.time)}</time>
                  </div>
                </div>
              ))}
              {!(release.events || []).length && (
                <div className="timeline-wait">
                  <span className="timeline-icon">
                    <Icon name="clock" size={13} />
                  </span>
                  <p>
                    {activeDeployment(release.status)
                      ? 'Waiting for the reconciler’s first event.'
                      : 'No additional events were recorded.'}
                  </p>
                </div>
              )}
            </div>
          </section>
          <section className="panel">
            <div className="panel-heading">
              <h2>Configuration diff</h2>
              <span className="muted-text">
                {release.revision === 1
                  ? 'Initial release'
                  : `r${release.revision - 1} → r${release.revision}`}
              </span>
            </div>
            <div className="panel-body">
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
                  configuration to inspect its current canonical specification.
                </Note>
              )}
            </div>
          </section>
        </div>
        <aside>
          <section className="panel">
            <div className="panel-heading">
              <h2>Service results</h2>
              <Icon name="box" size={16} />
            </div>
            <div className="release-services">
              {Object.entries(release.spec?.services || {}).map(([name, service]) => {
                const result = results.find((item) => item.name === name)
                const resolved = release.resolved_spec?.services?.[name]?.image
                return (
                  <div className="release-service" key={name}>
                    <div>
                      <strong>
                        <Icon name={service.public ? 'globe' : 'box'} size={15} />
                        {name}
                      </strong>
                      <Status value={result?.status || 'not observed'} small />
                    </div>
                    <span className="release-image-label">
                      {resolved ? 'RESOLVED IMAGE' : 'REQUESTED IMAGE'}
                    </span>
                    <code>{resolved || service.image}</code>
                    {resolved && <Copy value={resolved} label="Copy digest" />}
                    {result && (
                      <p>
                        {result.ready} / {result.desired} replicas ready
                      </p>
                    )}
                    {result?.message && <div className="service-message">{result.message}</div>}
                  </div>
                )
              })}
            </div>
          </section>
          <div className="deployment-explainer">
            <Icon name="shield" size={20} />
            <h3>A release you can trace.</h3>
            <p>
              Image digests and service configuration belong to this immutable revision. Rollback
              creates another recorded deployment.
            </p>
            <Link
              to="/applications/$applicationId"
              params={{ applicationId: release.application_id }}
            >
              View application
              <Icon name="arrow" size={14} />
            </Link>
          </div>
        </aside>
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
    </>
  )
}
