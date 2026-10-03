import { createFileRoute, Link, Outlet, useLocation, useNavigate } from '@tanstack/react-router'
import * as Tabs from '@radix-ui/react-tabs'
import { Empty, ErrorState, Loading, Note, PageHeader, Status } from '../components/shared'
import { Button } from '../components/ui/button'
import { Icon } from '../components/icons'
import { ServiceIcon } from '../components/service-icon'
import { PlatformRecoveryHistory } from '../components/platform-recovery'
import { PlatformSecurity } from '../components/platform-security'
import { useResourceScope, useScope, canAccess } from '../lib/scope'
import { useActiveSection } from '../lib/use-active-section'
import {
  managedPlatformName,
  platformSearch,
  useManagedPlatform,
  useManagedPlatformOperations,
} from '../lib/managed-platforms'

const platformTabs = ['overview', 'security', 'recovery', 'activity', 'configuration'] as const
type PlatformTab = (typeof platformTabs)[number]
export const Route = createFileRoute('/platforms/$platformId')({
  validateSearch: (
    search: Record<string, unknown>,
  ): { project: string; environment: string; tab?: PlatformTab } => ({
    ...platformSearch(search),
    tab: platformTabs.includes(search.tab as PlatformTab) ? (search.tab as PlatformTab) : undefined,
  }),
  component: Page,
})
function Page() {
  const id = Route.useParams().platformId
  return useLocation().pathname === `/platforms/${id}` ? <Detail /> : <Outlet />
}
function Detail() {
  const id = Route.useParams().platformId
  const query = useManagedPlatform(id)
  const { identity } = useScope()
  const search = Route.useSearch()
  const tab = search.tab || 'overview'
  const operations = useManagedPlatformOperations(id, tab === 'activity')
  const navigationRoot = useActiveSection(tab, '.tab-list')
  const navigate = useNavigate()
  useResourceScope(query.data)
  if (query.isPending) return <Loading />
  if (query.error && !query.data) return <ErrorState error={query.error} />
  const item = query.data!
  const scope = { project: item.project, environment: item.environment }
  const canManage = !identity.application && canAccess(identity, item.project, 'deployments:write')
  return (
    <div className="ops-page">
      <PageHeader
        title={item.spec.name}
        description={`${managedPlatformName(item.spec.kind)} ${item.spec.version}`}
        action={
          <div className="flex flex-wrap gap-2">
            <Button
              onClick={() => {
                void query.refetch()
                if (tab === 'activity') void operations.refetch()
              }}
              disabled={query.isFetching}
            >
              <Icon name="refresh" size={14} />
              Refresh
            </Button>
            {canManage && (
              <Button asChild variant="destructive">
                <Link to="/platforms/$platformId/delete" params={{ platformId: id }} search={scope}>
                  <Icon name="trash" size={14} />
                  Delete
                </Link>
              </Button>
            )}
          </div>
        }
      />
      {query.error && <Note>Refresh failed. Showing the last received platform state.</Note>}
      <div className="mb-4 flex min-w-0 flex-wrap items-center gap-x-4 gap-y-2 text-sm">
        <ServiceIcon name={item.spec.kind} size={24} />
        <span>{managedPlatformName(item.spec.kind)}</span>
        <Status value={item.status} />
        <span className="text-muted-foreground">Revision {item.revision}</span>
        <span className="text-muted-foreground break-all">
          {item.project} / {item.environment}
        </span>
      </div>
      <Tabs.Root
        value={tab}
        onValueChange={(value) =>
          void navigate({
            to: '/platforms/$platformId',
            params: { platformId: id },
            search: { ...scope, tab: value as PlatformTab },
            replace: true,
          })
        }
      >
        <Tabs.List ref={navigationRoot} className="tab-list" aria-label="Platform sections">
          {platformTabs.map((value) => (
            <Tabs.Trigger key={value} className="tab-trigger" value={value}>
              {value[0].toUpperCase() + value.slice(1)}
            </Tabs.Trigger>
          ))}
        </Tabs.List>
        <Tabs.Content value="overview" className="tab-content">
          <section className="db-panel">
            <div className="db-panel-heading">
              <h2>Runtime</h2>
            </div>
            <dl className="db-catalog-facts px-4">
              <div>
                <dt>Observed revision</dt>
                <dd>{item.observation.revision ?? 'Not observed'}</dd>
              </div>
              <div>
                <dt>Namespace</dt>
                <dd>{item.observation.namespace_uid ? 'Owned' : 'Not observed'}</dd>
              </div>
              <div>
                <dt>Ready components</dt>
                <dd>
                  {typeof item.observation.ready_components === 'number' &&
                  typeof item.observation.expected_components === 'number'
                    ? `${item.observation.ready_components} / ${item.observation.expected_components}`
                    : 'Not observed'}
                </dd>
              </div>
              <div>
                <dt>Phase</dt>
                <dd>{item.observation.phase || 'Not observed'}</dd>
              </div>
            </dl>
            {Boolean(item.observation.pending?.length) && (
              <div className="px-4 pb-4">
                <h3 className="mb-2 text-sm font-medium">Waiting for</h3>
                <ul className="grid gap-1 text-sm text-muted-foreground">
                  {item.observation.pending!.map((value) => (
                    <li key={value} className="break-all">
                      {value}
                    </li>
                  ))}
                </ul>
              </div>
            )}
          </section>
          {item.spec.kind === 'neon' && (
            <section className="db-panel mt-4">
              <div className="db-panel-heading">
                <h2>Neon storage</h2>
              </div>
              <dl className="db-catalog-facts px-4">
                <div>
                  <dt>Tenant</dt>
                  <dd className="break-all">{item.observation.tenant_id || 'Not observed'}</dd>
                </div>
                <div>
                  <dt>Timeline</dt>
                  <dd className="break-all">{item.observation.timeline_id || 'Not observed'}</dd>
                </div>
                <div>
                  <dt>Safekeepers</dt>
                  <dd>{item.observation.safekeeper_count ?? 'Not observed'}</dd>
                </div>
                <div>
                  <dt>Attached computes</dt>
                  <dd className="break-all">
                    {item.observation.attached_computes?.join(', ') || 'Not observed'}
                  </dd>
                </div>
              </dl>
            </section>
          )}
        </Tabs.Content>
        <Tabs.Content value="security" className="tab-content">
          <PlatformSecurity platform={item} stale={Boolean(query.error)} />
        </Tabs.Content>
        <Tabs.Content value="recovery" className="tab-content">
          <div className="flex flex-wrap items-center justify-between gap-3 py-3">
            <h2 className="text-base font-medium">Backups and recovery</h2>
            {canManage && (
              <div className="flex flex-wrap gap-2">
                <Button asChild>
                  <Link
                    to="/platforms/$platformId/restore"
                    params={{ platformId: id }}
                    search={scope}
                  >
                    Restore
                  </Link>
                </Button>
                <Button asChild variant="primary">
                  <Link
                    to="/platforms/$platformId/backup"
                    params={{ platformId: id }}
                    search={scope}
                  >
                    Back up
                  </Link>
                </Button>
              </div>
            )}
          </div>
          <PlatformRecoveryHistory id={id} canManage={canManage} />
        </Tabs.Content>
        <Tabs.Content value="activity" className="tab-content">
          {operations.error && operations.data && (
            <Note>Activity refresh failed. Showing the last received operations.</Note>
          )}
          {operations.isPending ? (
            <Loading />
          ) : operations.error && !operations.data ? (
            <ErrorState error={operations.error} />
          ) : !operations.data?.items.length ? (
            <Empty title="No operations" description="No lifecycle operation has been accepted." />
          ) : (
            <ul className="divide-y divide-border">
              {operations.data.items.map((op) => (
                <li key={op.id} className="min-w-0 py-4">
                  <div className="flex flex-wrap items-center gap-2">
                    <Status value={op.status} />
                    <span className="text-sm">{op.kind}</span>
                    <span className="ml-auto text-xs text-muted-foreground">
                      Revision {op.revision}
                    </span>
                  </div>
                  <p className="mt-2 text-sm text-muted-foreground break-all">
                    {op.phase} · Attempt {op.attempt}
                  </p>
                  {op.message && <p className="mt-1 text-sm break-all">{op.message}</p>}
                  <time
                    className="mt-2 block text-xs text-muted-foreground"
                    dateTime={op.created_at}
                  >
                    {new Date(op.created_at).toLocaleString()}
                  </time>
                </li>
              ))}
            </ul>
          )}
        </Tabs.Content>
        <Tabs.Content value="configuration" className="tab-content">
          <section className="db-panel">
            <div className="db-panel-heading">
              <h2>Requested resources</h2>
              {canManage && item.spec.kind === 'supabase' && (
                <Button asChild variant="primary">
                  <Link
                    to="/platforms/$platformId/configure"
                    params={{ platformId: id }}
                    search={scope}
                  >
                    Configure platform
                  </Link>
                </Button>
              )}
            </div>
            <div className="overflow-x-auto">
              <table className="w-full text-left text-sm">
                <thead>
                  <tr className="border-b border-border">
                    <th className="p-3 font-medium">Component</th>
                    <th className="p-3 font-medium">CPU</th>
                    <th className="p-3 font-medium">Memory</th>
                    <th className="p-3 font-medium">Storage</th>
                  </tr>
                </thead>
                <tbody>
                  {[
                    ...new Set([
                      ...Object.keys(item.spec.resources),
                      ...Object.keys(item.spec.storage),
                    ]),
                  ].map((name) => (
                    <tr key={name} className="border-b border-border last:border-0">
                      <th className="p-3 font-normal">{name.replaceAll('_', ' ')}</th>
                      <td className="p-3">{item.spec.resources[name]?.cpu || '—'}</td>
                      <td className="p-3">{item.spec.resources[name]?.memory || '—'}</td>
                      <td className="p-3">
                        {item.spec.storage[name] ? `${item.spec.storage[name]} GiB` : '—'}
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          </section>
          <section className="db-panel mt-4">
            <div className="db-panel-heading">
              <h2>Configured placement</h2>
            </div>
            <p className="px-4 pb-4 text-sm break-all">
              {item.spec.placement.node_names.join(', ')}
            </p>
          </section>
          <section className="db-panel mt-4">
            <div className="db-panel-heading">
              <h2>Secret references</h2>
            </div>
            <dl className="grid gap-3 px-4 pb-4 sm:grid-cols-2">
              {Object.entries(item.spec.secrets).map(([key, value]) => (
                <div key={key} className="min-w-0 text-sm">
                  <dt className="text-muted-foreground">{key.replaceAll('_', ' ')}</dt>
                  <dd className="break-all">
                    {value.name} · revision {value.revision}
                  </dd>
                </div>
              ))}
            </dl>
          </section>
        </Tabs.Content>
      </Tabs.Root>
    </div>
  )
}
