import { useEffect, useRef, useState } from 'react'
import { createFileRoute, Link, Outlet, useLocation, useNavigate } from '@tanstack/react-router'
import { useQueryClient } from '@tanstack/react-query'
import * as Tabs from '@radix-ui/react-tabs'
import { ArrowRight, LockKeyhole, Network, ShieldCheck, Trash2 } from 'lucide-react'
import { useExternalDatabase, useExternalDatabaseConnections, externalDatabaseHealth } from '../lib/external-databases'
import { useResourceScope, useScope, canAccess } from '../lib/scope'
import { useActiveSection } from '../lib/use-active-section'
import { client, unwrap } from '../lib/client'
import { message, timestamp } from '../lib/api'
import { bindingEvidence, connectedApplications } from '../lib/database-topology'
import { Copy, Empty, ErrorState, Loading, Note, PageHeader, Status } from '../components/shared'
import { FormError, FormSection } from '../components/form-page'
import { ServiceIcon } from '../components/service-icon'
import { Button } from '../components/ui/button'
import { Input } from '../components/ui/input'

const tabs = ['overview', 'connections', 'security', 'settings'] as const
type Tab = typeof tabs[number]
export const Route = createFileRoute('/databases/external/$externalDatabaseId')({
  validateSearch: (search: Record<string, unknown>): { tab?: Tab } => ({ tab: tabs.includes(search.tab as Tab) ? search.tab as Tab : undefined }),
  component: DetailRoute,
})
function DetailRoute() {
  const id = Route.useParams().externalDatabaseId
  return useLocation().pathname === `/databases/external/${id}` ? <Detail key={id} id={id} /> : <Outlet />
}
function Detail({ id }: { id: string }) {
  const query = useExternalDatabase(id)
  const connections = useExternalDatabaseConnections(id)
  useResourceScope(query.data)
  const tab = Route.useSearch().tab || 'overview'
  const navigationRoot = useActiveSection(tab, '.tab-list')
  const { identity } = useScope()
  const navigate = useNavigate()
  const cache = useQueryClient()
  const [now, setNow] = useState(Date.now)
  const [filter, setFilter] = useState('')
  const [page, setPage] = useState(0)
  const [confirmation, setConfirmation] = useState('')
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  const key = useRef('')
  useEffect(() => { const timer = setInterval(() => setNow(Date.now()), 5000); return () => clearInterval(timer) }, [])
  if (query.isPending) return <Loading />
  if (query.error && !query.data) return <ErrorState error={query.error} />
  const d = query.data!
  const search = { project: d.project, environment: d.environment }
  const params = { externalDatabaseId: id }
  const manage = !identity.application && canAccess(identity, d.project, 'deployments:write')
  const health = externalDatabaseHealth(d, now)
  const fresh = health === 'ready'
  const applications = connectedApplications(connections.data?.items || [])
  const filtered = applications.filter((app) => `${app.name} ${app.displayName}`.toLowerCase().includes(filter.toLowerCase()))
  const pageCount = Math.max(1, Math.ceil(filtered.length / 12))
  const currentPage = Math.min(page, pageCount - 1)
  const shown = filtered.slice(currentPage * 12, (currentPage + 1) * 12)
  return <div className="ops-page">
    <PageHeader title={d.spec.name} description="A legacy provider connection retained for inspection, credential rotation, disconnection and removal." action={manage && <div className="flex flex-wrap gap-2"><Button asChild><Link to="/databases/external/$externalDatabaseId/edit" params={params} search={search}>Rotate credentials</Link></Button>{(connections.data?.items.some((binding) => binding.saved_revision > 0) || connections.data?.truncated) && <Button asChild variant="primary"><Link to="/databases/external/$externalDatabaseId/connect" params={params} search={search}>Update application</Link></Button>}</div>} />
    <div className="flex flex-wrap items-center justify-between gap-3 pb-4"><div className="flex items-center gap-3"><ServiceIcon name={d.spec.engine} size={34} /><div><strong className="text-sm">Legacy external connection · {d.spec.engine === 'mysql' ? 'MySQL' : 'PostgreSQL'}</strong><p className="text-xs text-muted-foreground">Revision {d.revision}</p></div><Status value={health === 'Observation stale' ? 'stale' : health} /></div><Button disabled={query.isFetching} onClick={() => { void query.refetch(); void connections.refetch() }}>Refresh</Button></div>
    {query.error && <Note>Refresh failed. Showing the last received connection state.</Note>}
    {d.observation.message && <Note>{d.observation.message}</Note>}
    {health === 'Observation stale' && <Note>The last connection check is stale.</Note>}
    <Tabs.Root value={tab} onValueChange={(value) => void navigate({ to: '/databases/external/$externalDatabaseId', params, search: { ...search, tab: value as Tab } })}>
      <Tabs.List ref={navigationRoot} className="tab-list" aria-label="External database sections">{tabs.map((value) => <Tabs.Trigger key={value} className="tab-trigger" value={value}>{({ overview: 'Overview', connections: 'Applications', security: 'Security', settings: 'Settings' })[value]}</Tabs.Trigger>)}</Tabs.List>
      <Tabs.Content value="overview" className="grid gap-4 pt-4">
        <div className="grid gap-3 sm:grid-cols-2 xl:grid-cols-4">{[
          { label: 'TLS verification', value: fresh && d.observation.tls_verified ? 'Verified' : 'Not currently verified', icon: LockKeyhole },
          { label: 'Authenticated query', value: fresh && d.observation.query_verified ? 'Passed' : 'Not currently verified', icon: ShieldCheck },
          { label: 'Check duration', value: fresh && d.observation.latency_ms != null ? `${d.observation.latency_ms} ms` : 'Unavailable', icon: Network },
          { label: 'Application bindings', value: connections.data ? String(connections.data.items.length) : 'Unavailable', icon: ArrowRight },
        ].map(({ label, value, icon: Icon }) => <div key={label} className="db-layout-card"><Icon size={18} aria-hidden="true" /><span className="text-xs text-muted-foreground">{label}</span><strong>{value}</strong></div>)}</div>
        <FormSection title="Provider endpoint"><dl className="db-create-facts"><div><dt>Hostname</dt><dd className="flex min-w-0 items-center gap-2"><span className="break-all">{d.spec.host}</span><Copy value={d.spec.host} iconOnly /></dd></div><div><dt>Port</dt><dd>{d.spec.port}</dd></div><div><dt>Database</dt><dd>{d.spec.database}</dd></div><div><dt>Last check</dt><dd>{d.observation.revision ? timestamp(d.observation.observed_at) : 'Not observed'}</dd></div><div><dt>Credential revision</dt><dd>{d.credential_revision}</dd></div></dl></FormSection>
        <Note>This legacy record remains available for inspection, credential rotation and safe removal. New connections, endpoint changes and application bindings are unavailable. Query latency here is the duration of a connection check, not a database performance metric.</Note>
      </Tabs.Content>
      <Tabs.Content value="connections" className="grid gap-4 pt-4">
        <Note>These links come from saved application configurations and deployment history. They do not report live database sessions or the provider's internal topology.</Note>
        {connections.isPending ? <Loading /> : connections.error ? <ErrorState error={connections.error} /> : !applications.length ? <Empty title="No connected applications" description="This legacy connection can be removed when no active deployment still references it." /> : <>
          <Input aria-label="Find connected application" className="max-w-sm" placeholder="Find application…" value={filter} onChange={(event) => { setFilter(event.target.value); setPage(0) }} />
          <div className="grid gap-4 lg:grid-cols-[minmax(0,1fr)_minmax(240px,320px)]"><div className="grid gap-3 sm:grid-cols-2 xl:grid-cols-3">{shown.map((app) => <article key={app.id} className="db-layout-card"><strong className="break-words">{app.displayName || app.name}</strong>{app.bindings.map((binding) => <div key={`${binding.service}/${binding.variable}`} className="text-xs"><span className="break-all">{binding.service} · {binding.variable}</span><p className="text-muted-foreground">{bindingEvidence(binding)}</p></div>)}<Button asChild size="sm" variant="ghost"><Link to="/applications/$applicationId" params={{ applicationId: app.id }}>Open application<ArrowRight size={14} /></Link></Button></article>)}</div><div className="db-layout-card h-fit"><ServiceIcon name={d.spec.engine} size={40} /><strong>External provider</strong><span className="break-all">{d.spec.host}:{d.spec.port}</span><Status value={fresh ? 'ready' : health} /><span className="text-xs text-muted-foreground">TLS · provider-managed topology</span></div></div>
          {!shown.length && <Empty title="No matching applications" description="Try a different application name." />}
          {pageCount > 1 && <div className="flex items-center gap-3"><Button disabled={!currentPage} onClick={() => setPage(currentPage - 1)}>Previous</Button><span className="text-xs">Page {currentPage + 1} of {pageCount}</span><Button disabled={currentPage + 1 >= pageCount} onClick={() => setPage(currentPage + 1)}>Next</Button></div>}
          {connections.data?.truncated && <Note>The server's connection-list limit was reached. Inspect application configuration before removing this connection.</Note>}
        </>}
      </Tabs.Content>
      <Tabs.Content value="security" className="grid gap-4 pt-4">
        <FormSection title="Transport and credentials"><dl className="db-create-facts"><div><dt>Transport</dt><dd>TLS 1.2 or newer, with hostname and issuer verification</dd></div><div><dt>Trust source</dt><dd>Current public CA bundle</dd></div><div><dt>Certificate rotation</dt><dd>Managed by the provider</dd></div><div><dt>Credential storage</dt><dd>Encrypted at rest; excluded from configuration and API responses</dd></div><div><dt>Application access</dt><dd>Existing revision-pinned bindings only</dd></div></dl></FormSection>
        <Note>{d.spec.engine === 'mysql' ? 'Set your MySQL driver to verify the provider hostname and CA. Some drivers ignore URL TLS options; configure the driver directly and test refusal of an invalid certificate.' : 'Use sslmode=verify-full with a current public CA bundle. Keep the provider hostname in the connection even when routing to a resolved address.'}</Note>
        <Note>Removing a binding requires an application deployment. Revoke provider credentials only after every application has released them.</Note>
      </Tabs.Content>
      <Tabs.Content value="settings" className="grid gap-4 pt-4">
        <FormSection title="Connection ownership"><dl className="db-create-facts"><div><dt>Resource ID</dt><dd className="break-all">{d.id}</dd></div><div><dt>Scope</dt><dd>{d.project} / {d.environment}</dd></div><div><dt>Created</dt><dd>{timestamp(d.created_at)}</dd></div><div><dt>Updated</dt><dd>{timestamp(d.updated_at)}</dd></div></dl></FormSection>
        {manage && <FormSection title="Remove connection"><form className="grid gap-3" onSubmit={async (event) => {
          event.preventDefault(); if (busy || confirmation !== d.spec.name) return
          setBusy(true); setError('')
          try {
            if (!key.current) key.current = crypto.randomUUID()
            await unwrap(client.DELETE('/external-databases/{id}', { params: { path: { id }, header: { 'Idempotency-Key': key.current } }, body: { expected_revision: d.revision, confirm_name: confirmation } }))
            void cache.invalidateQueries({ queryKey: ['external-databases'] })
            void navigate({ to: '/databases', search })
          } catch (err) { setError(message(err)) } finally { setBusy(false) }
        }}><p className="text-sm">Disconnect applications and wait for their deployments to finish first. This removes Hakopod's saved connection. Provider data and credentials remain under your control.</p><label htmlFor="remove-external">Type {d.spec.name} to confirm<Input id="remove-external" autoComplete="off" value={confirmation} disabled={busy} onChange={(event) => { setConfirmation(event.target.value); key.current = ''; setError('') }} /></label>{error && <FormError>{error}</FormError>}<Button type="submit" variant="danger" className="w-fit" disabled={busy || confirmation !== d.spec.name}><Trash2 size={14} />{busy ? 'Removing…' : 'Remove connection'}</Button></form></FormSection>}
      </Tabs.Content>
    </Tabs.Root>
  </div>
}
