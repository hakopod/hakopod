import { useQuery } from '@tanstack/react-query'
import { Link, useNavigate } from '@tanstack/react-router'
import { Brackets } from '@hakopod/hatch-ui/components/brackets'
import { Menu, MenuItem } from '@hakopod/hatch-ui/components/dropdown-menu'
import { relative } from '../lib/api'
import { client, unwrap } from '../lib/client'
import type { Application } from '../lib/types'
import { actionsProvider, actionsProviderName, actionsTargetLabel } from '../lib/actions-provider'
import { Button } from './ui/button'
import { Badge } from '@hakopod/hatch-ui/components/badge'
import { HeadingHelp } from './shared'
import { Empty, ErrorState, Loading, Note } from './shared'
import { Icon } from './icons'
import { ServiceIcon } from './service-icon'

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
  const navigate = useNavigate()
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
  const config = item?.pool.config || application.spec.services[service]
  const actions = config?.actions
  const provider = actions ? actionsProvider(actions) : undefined
  const providerName = actions ? actionsProviderName(actions) : 'Provider'

  const fresh =
    item?.slots.filter((slot) => Date.now() - Date.parse(slot.updated_at) <= 120000) || []
  const busy = fresh.filter((slot) => slot.phase === 'busy').length
  const available = fresh.filter((slot) => slot.phase === 'online').length
  const starting = fresh.length - busy - available
  const stale = (item?.slots.length || 0) - fresh.length
  const replicas = item?.pool.config.replicas
  // Freshness comes from the newest backend observation, not from this browser's refetch.
  const observedAt = item
    ? [item.pool.updated_at, ...item.slots.map((slot) => slot.updated_at)].sort().at(-1)
    : undefined
  const known = !!item && !query.error && fresh.length > 0
  const state = query.isPending
    ? { tone: 'pending', label: 'Checking pool' }
    : query.error
      ? { tone: 'unknown', label: 'Status unavailable' }
      : !item
        ? { tone: 'pending', label: 'Waiting for reconciliation' }
        : removing
          ? { tone: 'pending', label: 'Removal pending' }
          : !fresh.length
            ? stale
              ? { tone: 'warning', label: 'Observation out of date' }
              : { tone: 'pending', label: 'No runners registered' }
            : busy
              ? { tone: 'success', label: busy === 1 ? 'Running 1 job' : `Running ${busy} jobs` }
              : available
                ? { tone: 'success', label: 'Idle' }
                : { tone: 'pending', label: 'Starting' }
  const registrations = `runner-registrations-${service}`
  const jobsLink = {
    to: '/applications/$applicationId' as const,
    params: { applicationId: application.id },
    search: { service, tab: 'actions' },
  }
  return (
    <section className="runner-pool-grid mb-6" aria-label={`${providerName} runner pool`}>
      <article className="runner-pool-card interactive">
        <Brackets />
        <div className="runner-pool-card-head">
          <span className="runner-pool-type" aria-hidden="true">
            <ServiceIcon name={provider || 'box'} size={20} />
          </span>
          <h2 className="min-w-0 flex-1">
            {removing ? (
              <span className="wrap-anywhere">{service}</span>
            ) : (
              <Link {...jobsLink} className="runner-pool-name wrap-anywhere">
                {service}
              </Link>
            )}
            <HeadingHelp title="Runner pool">
              {provider === 'bitbucket'
                ? 'This runner stays assigned to one repository. Its sandbox is reused between pipeline steps.'
                : 'Each job uses a fresh workspace. Workflow cache configuration controls what is restored between jobs.'}
            </HeadingHelp>
          </h2>
          <Menu
            trigger={
              <Button variant="ghost" size="icon" aria-label={`Actions for ${service}`}>
                <span aria-hidden="true">···</span>
              </Button>
            }
          >
            {!removing && (
              <MenuItem onSelect={() => void navigate(jobsLink)}>
                <Icon name="activity" size={14} />
                View jobs
              </MenuItem>
            )}
            <MenuItem
              onSelect={() => {
                const details = document.getElementById(registrations)
                if (!(details instanceof HTMLDetailsElement)) return
                details.open = true
                details.querySelector('summary')?.focus()
              }}
            >
              <Icon name="server" size={14} />
              Runner registrations
            </MenuItem>
          </Menu>
        </div>
        <p className="runner-pool-meta">
          {providerName} ·{' '}
          {config?.architecture ? config.architecture.toUpperCase() : 'Automatic architecture'} ·{' '}
          {config?.node_name || 'Any eligible node'}
        </p>
        <p className="runner-pool-scope wrap-anywhere">
          {actions ? actionsTargetLabel(actions) : 'Scope unavailable'}
        </p>
        <p className="runner-pool-state" data-tone={state.tone} role="status">
          <span className="runner-pool-dot" aria-hidden="true" />
          {state.label}
          {observedAt && !query.error && (
            <time className="muted-text" dateTime={observedAt}>
              · checked {relative(observedAt).toLowerCase()}
            </time>
          )}
        </p>
        <div className="runner-pool-metric">
          <span className="muted-text">Running jobs · last 2 min</span>
          <strong className="tabular-nums">
            {known ? `${busy} of ${replicas ?? '—'} slots` : 'Unknown'}
          </strong>
          {known && replicas ? (
            <span className="runner-meter" aria-hidden="true">
              <i style={{ width: `${Math.min(100, (busy / replicas) * 100)}%` }} />
            </span>
          ) : null}
        </div>
      </article>
      <div className="grid min-w-0 content-start gap-3">
        {query.isPending ? (
          <Loading rows={1} />
        ) : query.error ? (
          <ErrorState error={query.error} retry={() => void query.refetch()} />
        ) : !item ? (
          <Empty
            title="Waiting for pool reconciliation"
            description="Registrations appear after the runner sandbox is ready."
          />
        ) : (
          <>
            <div className="flex min-w-0 flex-wrap items-center gap-x-4 gap-y-2 text-xs">
              {available > 0 && busy > 0 && (
                <span>
                  <strong>{available}</strong> available
                </span>
              )}
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
            <details id={registrations} className="group/registrations min-w-0">
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
                      <li key={slot.id} className="runner-row">
                        <code className="break-all">hakopod-{slot.id}</code>
                        <span className={outdated ? 'text-[var(--warning)]' : undefined}>
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
                Keep this application's credential until {providerName} registration cleanup
                finishes.
              </p>
            )}
          </>
        )}
      </div>
    </section>
  )
}
