import { useQuery } from '@tanstack/react-query'
import { Link } from '@tanstack/react-router'
import { client, unwrap } from '../lib/client'
import type { Application } from '../lib/types'
import { canAccess, useScope } from '../lib/scope'
import { actionsProvider, actionsProviderName, actionsTargetLabel } from '../lib/actions-provider'
import { Button } from './ui/button'
import { Badge } from '@hakopod/hatch-ui/components/badge'
import { HeadingHelp } from './shared'
import { Empty, ErrorState, Loading, Note } from './shared'

export function ManagedActionsStatus({
  application,
  service,
}: {
  application: Application
  service?: string
}) {
  const scope = useScope()
  const query = useQuery({
    queryKey: ['managed-actions', application.id],
    queryFn: ({ signal }) =>
      unwrap(
        client.GET('/applications/{id}/actions', {
          signal,
          params: { path: { id: application.id } },
        }),
      ),
    refetchInterval: 10000,
    refetchIntervalInBackground: false,
    gcTime: 0,
  })
  const item = query.data?.items.find((item) => item.pool.service === service)
  if (!service) {
    if (query.error) return <ErrorState error={query.error} retry={() => void query.refetch()} />
    return (
      <>
        {query.data?.items
          .filter((item) => item.pool.removed)
          .map((item) => (
            <ManagedActionsStatus
              key={item.pool.service}
              application={application}
              service={item.pool.service}
            />
          ))}
      </>
    )
  }
  const removing = item?.pool.removed || !application.spec.services[service]
  const actions = item?.pool.config.actions || application.spec.services[service]?.actions
  const provider = actions ? actionsProvider(actions) : undefined
  const providerName = actions ? actionsProviderName(actions) : 'Provider'

  const fresh =
    item?.slots.filter((slot) => Date.now() - Date.parse(slot.updated_at) <= 120000) || []
  const busy = fresh.filter((slot) => slot.phase === 'busy').length
  const available = fresh.filter((slot) => slot.phase === 'online').length
  const starting = fresh.length - busy - available
  const stale = (item?.slots.length || 0) - fresh.length
  return (
    <section
      className="mb-4 grid min-w-0 gap-3 border-b border-[var(--hairline)] pb-4"
      aria-label={`${providerName} runner pool`}
    >
      <div className="flex min-w-0 flex-wrap items-center justify-between gap-3">
        <div className="flex min-w-0 flex-wrap items-center gap-2">
          <h2 className="text-sm font-semibold">{providerName} runners</h2>
          {actions && (
            <Badge>
              <span className="min-w-0 break-all whitespace-normal">
                {actionsTargetLabel(actions)}
              </span>
            </Badge>
          )}
          <HeadingHelp title="Runner pool">
            {provider === 'bitbucket'
              ? 'This runner stays assigned to one repository. Its sandbox is reused between pipeline steps.'
              : 'Each job uses a fresh workspace. Workflow cache configuration controls what is restored between jobs.'}
          </HeadingHelp>
        </div>
        {!removing && (
          <div className="flex flex-wrap items-center gap-2">
            <Button size="sm" variant="ghost" asChild>
              <Link
                to="/applications/$applicationId"
                params={{ applicationId: application.id }}
                search={{ service, tab: 'actions' }}
              >
                Workflow activity
              </Link>
            </Button>
            {canAccess(scope.identity, application.project, 'deployments:write') && (
              <Button size="sm" asChild>
                <Link
                  to="/templates/$templateId"
                  params={{ templateId: 'managed-actions' }}
                  search={{ application: application.id, runner: service }}
                >
                  Configure pool
                </Link>
              </Button>
            )}
          </div>
        )}
      </div>
      {query.isPending ? (
        <Loading />
      ) : query.error ? (
        <ErrorState error={query.error} retry={() => void query.refetch()} />
      ) : !item ? (
        <Empty
          title="Waiting for pool reconciliation"
          description="Registrations appear after the runner sandbox is ready."
        />
      ) : (
        <>
          <div
            className="flex flex-wrap items-center gap-x-5 gap-y-2 text-sm"
            aria-label="Runner pool status"
          >
            <span>
              <strong>{busy}</strong> running
            </span>
            <span>
              <strong>{available}</strong> available
            </span>
            {starting > 0 && (
              <span>
                <strong>{starting}</strong> starting or cleaning up
              </span>
            )}
            {stale > 0 && (
              <span className="text-[var(--warning)]">
                <strong>{stale}</strong> out of date
              </span>
            )}
            <span className="muted-text">{item.pool.config.replicas} job slots</span>
            {removing && <Badge>Removal pending</Badge>}
            {!removing && (provider === 'github' || provider === 'gitlab') && (
              <code className="min-w-0 break-all text-xs muted-text">
                {provider === 'gitlab' ? 'tags' : 'runs-on'}: [
                {item.pool.config.actions?.labels.join(', ')}]
              </code>
            )}
          </div>
          {item.pool.message && <Note>{item.pool.message}</Note>}
          <details className="min-w-0">
            <summary className="flex min-h-11 cursor-pointer items-center gap-2 text-xs muted-text">
              Inspect {item.slots.length} runner{' '}
              {item.slots.length === 1 ? 'registration' : 'registrations'}
            </summary>
            {item.slots.length === 0 ? (
              <p className="py-2 text-sm muted-text">No runners are currently registered.</p>
            ) : (
              <ul className="grid min-w-0" aria-label="Runner registrations">
                {item.slots.map((slot) => {
                  const outdated = Date.now() - Date.parse(slot.updated_at) > 120000
                  return (
                    <li
                      key={slot.id}
                      className="grid min-w-0 gap-1 border-t border-[var(--hairline)] py-2 text-xs sm:grid-cols-[minmax(0,1fr)_auto_auto] sm:gap-4"
                    >
                      <code className="break-all">hakopod-{slot.id}</code>
                      <span>
                        {outdated
                          ? 'Observation out of date'
                          : slot.phase === 'busy'
                            ? 'Running a job'
                            : slot.phase === 'online'
                              ? 'Available'
                              : slot.phase === 'cleanup'
                                ? 'Removing registration'
                                : 'Registering'}
                      </span>
                      <time className="muted-text" dateTime={slot.updated_at}>
                        Checked {new Date(slot.updated_at).toLocaleTimeString()}
                      </time>
                    </li>
                  )
                })}
              </ul>
            )}
          </details>
          {removing && (
            <p className="text-sm muted-text">
              Keep this application's credential until {providerName} registration cleanup finishes.
            </p>
          )}
        </>
      )}
    </section>
  )
}
