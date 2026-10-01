import { useQuery } from '@tanstack/react-query'
import { Link } from '@tanstack/react-router'
import { client, unwrap } from '../lib/client'
import type { Application } from '../lib/types'
import { actionsProvider, actionsProviderName, actionsTargetLabel } from '../lib/actions-provider'
import { Button } from './ui/button'
import { Badge } from '@hakopod/hatch-ui/components/badge'
import { HeadingHelp } from './shared'
import { Empty, ErrorState, Loading, Note } from './shared'
import { Icon } from './icons'

export function ManagedActionsStatus({
  application,
  service,
}: {
  application: Application
  service?: string
}) {
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
      className="mb-6 grid min-w-0 gap-4 border-b border-[var(--hairline)] pb-4"
      aria-label={`${providerName} runner pool`}
    >
      <div className="flex min-w-0 flex-wrap items-center justify-between gap-3">
        <div className="flex min-w-0 flex-wrap items-center gap-2">
          <div className="flex items-center gap-2">
            <h2 className="text-sm font-semibold">{providerName} runners</h2>
            <HeadingHelp title="Runner pool">
              {provider === 'bitbucket'
                ? 'This runner stays assigned to one repository. Its sandbox is reused between pipeline steps.'
                : 'Each job uses a fresh workspace. Workflow cache configuration controls what is restored between jobs.'}
            </HeadingHelp>
          </div>
          {actions && (
            <Badge>
              <span className="min-w-0 break-all whitespace-normal">
                {actionsTargetLabel(actions)}
              </span>
            </Badge>
          )}
        </div>
        {!removing && (
          <div className="flex flex-wrap items-center gap-2">
            <Button size="sm" variant="primary" asChild>
              <Link
                to="/applications/$applicationId"
                params={{ applicationId: application.id }}
                search={{ service, tab: 'actions' }}
              >
                View jobs
                <Icon name="arrow" size={14} />
              </Link>
            </Button>
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
          <dl
            className="grid min-w-0 grid-cols-3 gap-4 sm:max-w-lg"
            aria-label="Runner pool status"
          >
            <div className="grid gap-1">
              <dt className="min-h-8 text-xs muted-text sm:min-h-0">Running jobs</dt>
              <dd className="text-2xl font-medium tabular-nums">{busy}</dd>
            </div>
            <div className="grid gap-1">
              <dt className="min-h-8 text-xs muted-text sm:min-h-0">Available runners</dt>
              <dd className="text-2xl font-medium tabular-nums">{available}</dd>
            </div>
            <div className="grid gap-1">
              <dt className="min-h-8 text-xs muted-text sm:min-h-0">Configured slots</dt>
              <dd className="text-2xl font-medium tabular-nums">{item.pool.config.replicas}</dd>
            </div>
          </dl>
          <div className="flex min-w-0 flex-wrap items-center gap-x-4 gap-y-2 text-xs">
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
            {removing && <Badge>Removal pending</Badge>}
            {!removing && (provider === 'github' || provider === 'gitlab') && (
              <div
                className="flex min-w-0 flex-wrap items-center gap-2"
                aria-label="Workflow labels"
              >
                <span className="muted-text">
                  {provider === 'gitlab' ? 'Tags' : 'Workflow labels'}
                </span>
                {item.pool.config.actions?.labels.map((label) => (
                  <Badge key={label}>
                    <span className="break-all whitespace-normal">{label}</span>
                  </Badge>
                ))}
              </div>
            )}
          </div>
          {item.pool.message && <Note>{item.pool.message}</Note>}
          <details className="group/registrations min-w-0">
            <summary className="flex min-h-11 cursor-pointer list-none items-center gap-2 text-xs muted-text [&::-webkit-details-marker]:hidden">
              <Icon
                name="chevron"
                size={14}
                className="shrink-0 group-open/registrations:rotate-90"
              />
              {item.slots.length} runner{' '}
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
