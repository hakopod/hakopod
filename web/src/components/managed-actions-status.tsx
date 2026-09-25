import { useQuery } from '@tanstack/react-query'
import { Link } from '@tanstack/react-router'
import { client, unwrap } from '../lib/client'
import type { Application } from '../lib/types'
import { canAccess, useScope } from '../lib/scope'
import { Button } from './ui/button'
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

  return (
    <section className="panel mb-4">
      <div className="panel-heading">
        <h2>GitHub runners</h2>
        {!removing && canAccess(scope.identity, application.project, 'deployments:write') && (
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
      <div className="grid gap-3 p-4">
        {query.isPending ? (
          <Loading />
        ) : query.error ? (
          <ErrorState error={query.error} retry={() => void query.refetch()} />
        ) : !item ? (
          <Empty
            title="Waiting for pool reconciliation"
            description="Runner registrations appear after the deployment reaches the sandbox setup step."
          />
        ) : (
          <>
            <p className="text-sm min-w-0 wrap-anywhere">
              {item.pool.config.actions?.repository} ·{' '}
              {removing ? 'Removal pending' : `${item.pool.config.replicas} configured job slots`}
            </p>
            {item.pool.message && <Note>{item.pool.message}</Note>}
            {item.slots.length === 0 ? (
              <p className="text-sm muted-text">No runners are currently registered.</p>
            ) : (
              <ul className="grid gap-2" aria-label="Runner registrations">
                {item.slots.map((slot) => {
                  const stale = Date.now() - Date.parse(slot.updated_at) > 120000
                  return (
                    <li
                      key={slot.id}
                      className="flex flex-wrap items-center justify-between gap-2 border-b pb-2 text-sm"
                    >
                      <code className="break-all">hakopod-{slot.id}</code>
                      <span>
                        {stale
                          ? 'Observation out of date'
                          : slot.phase === 'busy'
                            ? 'Running a GitHub job'
                            : slot.phase === 'online'
                              ? 'Online in GitHub'
                              : slot.phase === 'cleanup'
                                ? 'Removing registration'
                                : 'Registering with GitHub'}
                      </span>
                      <time className="muted-text" dateTime={slot.updated_at}>
                        Checked {new Date(slot.updated_at).toLocaleTimeString()}
                      </time>
                    </li>
                  )
                })}
              </ul>
            )}
            {removing ? (
              <p className="text-sm muted-text">
                GitHub registrations are being removed. Keep this application's credential until
                cleanup finishes; the application can then be deleted.
              </p>
            ) : (
              <p className="text-sm muted-text">
                Use these labels in your workflow:{' '}
                <code className="break-all">
                  runs-on: [{item.pool.config.actions?.labels.join(', ')}]
                </code>
                . Each runner handles one job, then receives a fresh workspace. This pool does not
                change Hakopod's application build provider.
              </p>
            )}
          </>
        )}
      </div>
    </section>
  )
}
