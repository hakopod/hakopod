import { useEffect, useRef, useState, type ReactNode } from 'react'
import { createFileRoute, Link, Outlet, useLocation, useNavigate } from '@tanstack/react-router'
import { useQueryClient } from '@tanstack/react-query'
import * as Tabs from '@radix-ui/react-tabs'
import { useExternalDatabase, useExternalDatabaseConnections, externalDatabaseHealth } from '../lib/external-databases'
import { useResourceScope, useScope, canAccess } from '../lib/scope'
import { useActiveSection } from '../lib/use-active-section'
import { client, unwrap } from '../lib/client'
import { message, relative, timestamp } from '../lib/api'
import { bindingEvidence, connectedApplications } from '../lib/database-topology'
import { Copy, ErrorState, HeadingHelp, Loading, Note, Status } from '../components/shared'
import { FormError } from '../components/form-page'
import { Icon } from '../components/icons'
import { ResourceEmpty, ResourceError, ResourceRow, ResourceRows } from '../components/resource-card'
import { CopyValue, ReviewFacts, ReviewNotice } from '../components/operation-review'
import { ServiceIcon } from '../components/service-icon'
import { Button } from '../components/ui/button'
import { Input } from '../components/ui/input'

const tabs = ['overview', 'connections', 'settings'] as const
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
  const at = d.observation.revision ? d.observation.observed_at : undefined
  const state = health === 'Observation stale' ? 'stale' : health
  const problem = state !== 'ready' || Boolean(query.error) || Boolean(d.observation.message)
  const facts: [string, ReactNode][] = [
    ['Hostname', <span key="host" className="db-id"><code title={d.spec.host}>{d.spec.host}</code><CopyValue value={d.spec.host} what="provider hostname" /></span>],
    ['Port', String(d.spec.port)],
    ['Database', d.spec.database],
    ['TLS verification', fresh && d.observation.tls_verified ? 'Verified' : 'Not currently verified'],
    ['Authenticated query', fresh && d.observation.query_verified ? 'Passed' : 'Not currently verified'],
    ['Check duration', fresh && d.observation.latency_ms != null ? `${d.observation.latency_ms} ms` : 'Unavailable'],
    ['Last check', at ? timestamp(at) : 'Not observed'],
    ['Credential revision', String(d.credential_revision)],
    ['Resource ID', <span key="id" className="db-id"><code title={d.id}>{d.id.slice(0, 8)}</code><Copy iconOnly value={d.id} label="Copy resource ID" /></span>],
    ['Created', timestamp(d.created_at)],
  ]
  return <div className="ops-page app-workspace db-workspace">
    <header className="app-workspace-head">
      <div className="app-workspace-identity">
        <nav className="app-workspace-scope" aria-label="Database scope">
          <Link to="/projects/$project" params={{ project: d.project }}>{d.project}</Link>
          <span aria-hidden="true">/</span>
          <span>{d.environment}</span>
          <span aria-hidden="true">/</span>
          <Link to="/databases" search={search}>Databases</Link>
        </nav>
        <div className="app-workspace-title">
          <ServiceIcon name={d.spec.engine} size={24} />
          <h1>{d.spec.name}</h1>
          <HeadingHelp title="legacy provider connections">
            A legacy provider connection is kept for inspection, credential rotation, disconnection
            and removal. New connections, endpoint changes and application bindings are unavailable.
            Check duration is the time a connection check took, not a database performance metric.
          </HeadingHelp>
        </div>
        <div className="app-workspace-meta">
          <p className="db-health-line" role="status">
            <Status value={state} small />
            {at ? <time dateTime={at} title={timestamp(at)}>Checked {relative(at).toLowerCase()}</time> : <span>Not checked yet</span>}
          </p>
          <span>Legacy external connection · {d.spec.engine === 'mysql' ? 'MySQL' : 'PostgreSQL'}</span>
          <span>Revision {d.revision}</span>
        </div>
      </div>
      {manage && <div className="app-workspace-actions">
        <Button asChild><Link to="/databases/external/$externalDatabaseId/edit" params={params} search={search}>Rotate credentials</Link></Button>
        {(connections.data?.items.some((binding) => binding.saved_revision > 0) || connections.data?.truncated) && <Button asChild variant="primary"><Link to="/databases/external/$externalDatabaseId/connect" params={params} search={search}>Update application</Link></Button>}
      </div>}
    </header>
    <Tabs.Root value={tab} onValueChange={(value) => void navigate({ to: '/databases/external/$externalDatabaseId', params, search: { ...search, tab: value as Tab } })}>
      <Tabs.List ref={navigationRoot} className="tab-list" aria-label="External database sections">{tabs.map((value) => <Tabs.Trigger key={value} className="tab-trigger" value={value}>{({ overview: 'Overview', connections: 'Applications', settings: 'Remove' })[value]}</Tabs.Trigger>)}</Tabs.List>
      <Tabs.Content value="overview" className="tab-content app-workspace-overview">
        {problem && <section className={`app-attention db-attention db-attention-${state === 'stale' || state === 'Not observed' ? 'warn' : 'error'}`} aria-labelledby="ext-attention-heading">
          <Icon name="alert" size={18} />
          <div className="app-attention-body">
            <h2 id="ext-attention-heading">{state === 'stale' ? 'The last connection check is stale' : state === 'ready' ? 'Refresh failed' : `${d.spec.name} is ${state.toLowerCase()}`}</h2>
            {d.observation.message && <p className="app-attention-log"><code>{d.observation.message}</code></p>}
            {query.error && <p className="app-attention-detail">Refresh failed. Showing the last received connection state.</p>}
            <div className="app-attention-actions"><Button size="sm" disabled={query.isFetching} onClick={() => { void query.refetch(); void connections.refetch() }}><Icon name="refresh" size={14} />Check again</Button></div>
          </div>
        </section>}
        <section className="app-workspace-section" aria-labelledby="ext-endpoint-heading">
          <div className="app-workspace-section-head">
            <h2 id="ext-endpoint-heading">Provider endpoint</h2>
            <HeadingHelp title="provider transport">
              TLS 1.2 or newer, with hostname and issuer verification against the current public CA
              bundle. The provider manages certificate rotation. Credentials are encrypted at rest
              and excluded from configuration and API responses.{' '}
              {d.spec.engine === 'mysql' ? 'Set your MySQL driver to verify the provider hostname and CA. Some drivers ignore URL TLS options; configure the driver directly and test refusal of an invalid certificate.' : 'Use sslmode=verify-full with a current public CA bundle. Keep the provider hostname in the connection even when routing to a resolved address.'}
            </HeadingHelp>
          </div>
          <dl className="app-workspace-details">{facts.map(([term, value]) => <div key={term}><dt>{term}</dt><dd>{value}</dd></div>)}</dl>
        </section>
      </Tabs.Content>
      <Tabs.Content value="connections" className="tab-content app-workspace-overview">
        <section className="app-workspace-section" aria-labelledby="ext-apps-heading">
          <div className="app-workspace-section-head">
            <h2 id="ext-apps-heading">Connected applications</h2>
            <HeadingHelp title="connected applications">These links come from saved application configurations and deployment history. They do not report live database sessions or the provider's internal topology. Removing a binding requires an application deployment.</HeadingHelp>
            <span className="app-workspace-count">{connections.data ? applications.length : ''}</span>
          </div>
          {connections.isPending ? <Loading /> : connections.error ? <ResourceError error={connections.error} retry={() => void connections.refetch()} subject="connected applications" /> : !applications.length ? <ResourceEmpty icon="box" title="No connected applications" description="This legacy connection can be removed when no active deployment references it." /> : <>
            {applications.length > 12 && <div className="ops-search-input"><Icon name="search" size={15} /><Input aria-label="Find connected application" placeholder="Find application…" value={filter} onChange={(event) => { setFilter(event.target.value); setPage(0) }} /></div>}
            {!shown.length ? <ResourceEmpty icon="search" title="No matching applications" description="Try a different application name." /> : <ResourceRows label="Connected applications">{shown.map((app) => <ResourceRow key={app.id}
              identity={<Link className="resource-row-link" to="/applications/$applicationId" params={{ applicationId: app.id }}>{app.displayName || app.name}</Link>}
              detail={app.bindings.map((binding) => <span key={`${binding.service}/${binding.variable}`}>{binding.service} · {binding.variable} · {bindingEvidence(binding)}</span>)}
            />)}</ResourceRows>}
            {pageCount > 1 && <div className="flex items-center gap-3"><Button disabled={!currentPage} onClick={() => setPage(currentPage - 1)}>Previous</Button><span className="text-xs">Page {currentPage + 1} of {pageCount}</span><Button disabled={currentPage + 1 >= pageCount} onClick={() => setPage(currentPage + 1)}>Next</Button></div>}
            {connections.data?.truncated && <Note>The server's connection-list limit was reached. Inspect application configuration before removing this connection.</Note>}
          </>}
        </section>
      </Tabs.Content>
      <Tabs.Content value="settings" className="tab-content app-workspace-overview">
        <section className="app-workspace-section" aria-labelledby="ext-remove-heading">
          <div className="app-workspace-section-head"><h2 id="ext-remove-heading">Remove connection</h2></div>
          {!manage ? <ResourceEmpty icon="lock" title="Removing requires deployment access" description="Ask a project administrator for access." /> : <form className="ext-remove-form" onSubmit={async (event) => {
            event.preventDefault(); if (busy || confirmation !== d.spec.name) return
            setBusy(true); setError('')
            try {
              if (!key.current) key.current = crypto.randomUUID()
              await unwrap(client.DELETE('/external-databases/{id}', { params: { path: { id }, header: { 'Idempotency-Key': key.current } }, body: { expected_revision: d.revision, confirm_name: confirmation } }))
              void cache.invalidateQueries({ queryKey: ['external-databases'] })
              void navigate({ to: '/databases', search })
            } catch (err) { setError(message(err)) } finally { setBusy(false) }
          }}>
            <ReviewFacts facts={[['Scope', `${d.project} / ${d.environment}`], ['Connected applications', connections.data ? String(applications.length) : 'Unavailable'], ['Removes', "Hakopod's saved connection and encrypted credentials"], ['Keeps', 'Provider data and provider credentials']]} />
            <ReviewNotice tone="warn" title="Disconnect applications first">Wait for their deployments to finish. Revoke provider credentials only after every application has released them.</ReviewNotice>
            <label className="db-confirm-field" htmlFor="remove-external"><span>Type {d.spec.name} to confirm</span><Input id="remove-external" autoComplete="off" value={confirmation} disabled={busy} onChange={(event) => { setConfirmation(event.target.value); key.current = ''; setError('') }} /></label>
            {error && <FormError>{error}</FormError>}
            <div><Button type="submit" variant="danger" aria-busy={busy || undefined} disabled={busy || confirmation !== d.spec.name}><Icon name="trash" size={14} />{busy ? 'Removing…' : `Remove ${d.spec.name}`}</Button></div>
          </form>}
        </section>
      </Tabs.Content>
    </Tabs.Root>
  </div>
}
