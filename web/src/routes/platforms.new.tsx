import { useEffect, useRef, useState } from 'react'
import { createFileRoute, Link, useNavigate } from '@tanstack/react-router'
import { useQueryClient } from '@tanstack/react-query'
import { FormError, FormPage, FormSection } from '../components/form-page'
import { Empty, ErrorState, Loading, Note, Status } from '../components/shared'
import { Button } from '../components/ui/button'
import { Input } from '../components/ui/input'
import { SelectField } from '../components/ui/select'
import { message, timestamp } from '../lib/api'
import { client, unwrap } from '../lib/client'
import { canAccess, useScope } from '../lib/scope'
import { availableManagedPlatformEntries, managedPlatformName, platformSearch, useManagedPlatformCatalog, type ManagedPlatformCatalog, type ManagedPlatformDefaults, type ManagedPlatformReviewResponse, type ManagedPlatformSpec } from '../lib/managed-platforms'
import { projectRouteScopeMatches } from '../lib/projects'

export const Route = createFileRoute('/platforms/new')({ validateSearch: platformSearch, component: Page })

function Page() {
  const scope = Route.useSearch()
  if (!scope.project || !scope.environment) return <Empty title="Choose a project and environment" description="Open Platforms from the project where this platform belongs." />
  return <CreatePlatform key={`${scope.project}/${scope.environment}`} {...scope} />
}

function CreatePlatform({ project, environment }: { project: string; environment: string }) {
  const workspace = useScope()
  const { identity } = workspace
  const scopeMatches = projectRouteScopeMatches(workspace, { project, environment })
  const canManage = !identity.application && canAccess(identity, project, 'deployments:write')
  const query = useManagedPlatformCatalog(project, environment, canManage && scopeMatches)
  if (!scopeMatches) return <Empty title="Workspace unavailable" description="Choose an accessible project and environment above." />
  if (!identity.application && !canManage) return <ErrorState error={new Error('You do not have permission to create platforms in this project.')} />
  if (identity.application) return <ErrorState error={new Error('Application-scoped access cannot create managed platforms.')} />
  if (query.isPending) return <Loading />
  if (query.error) return <ErrorState error={query.error} />
  const items = availableManagedPlatformEntries(query.data)
  if (!items.length) return <Empty title="Platform creation unavailable" description="No managed platforms have completed the required checks on this server." />
  return <PlatformForm catalog={{ ...query.data, items }} />
}

function PlatformForm({ catalog }: { catalog: ManagedPlatformCatalog }) {
  const [kind, setKind] = useState<'supabase' | 'neon'>(catalog.items[0]?.kind || 'supabase')
  const selected = catalog.items.find(item => item.kind === kind)
  const [drafts, setDrafts] = useState<{
    supabase?: Extract<ManagedPlatformDefaults, { kind: 'supabase' }>
    neon?: Extract<ManagedPlatformDefaults, { kind: 'neon' }>
  }>(() => {
    const values: {
      supabase?: Extract<ManagedPlatformDefaults, { kind: 'supabase' }>
      neon?: Extract<ManagedPlatformDefaults, { kind: 'neon' }>
    } = {}
    for (const item of catalog.items) {
      if (item.default_spec.kind === 'supabase') values.supabase = item.default_spec
      else values.neon = item.default_spec
    }
    return values
  })
  const [nodesByKind, setNodesByKind] = useState<{ supabase: string[]; neon: string[] }>({ supabase: [], neon: [] })
  const [secretsByKind, setSecretsByKind] = useState<{ supabase: Record<string, string>; neon: Record<string, string> }>({ supabase: {}, neon: {} })
  const [review, setReview] = useState<ManagedPlatformReviewResponse | null>(null)
  const [confirmed, setConfirmed] = useState(false)
  const [busy, setBusy] = useState(false)
  const [, refreshExpiry] = useState(0)
  const [error, setError] = useState('')
  const idempotency = useRef('')
  const cache = useQueryClient()
  const navigate = useNavigate()
  useEffect(() => {
    const deadline = Date.parse(review?.review?.expires_at || '')
    if (!Number.isFinite(deadline)) return
    const timer = window.setTimeout(() => {
      setConfirmed(false)
      refreshExpiry(value => value + 1)
    }, Math.min(Math.max(deadline - Date.now(), 0), 2_147_483_647))
    return () => window.clearTimeout(timer)
  }, [review?.review?.expires_at])
  if (!selected) return <ErrorState error={new Error('The selected platform configuration is no longer available.')} />
  const entry = selected
  const draft = kind === 'supabase' ? drafts.supabase : drafts.neon
  if (!draft) return <ErrorState error={new Error('The selected platform defaults are unavailable.')} />
  const nodes = nodesByKind[kind]
  const secrets = secretsByKind[kind]
  const expired = Boolean(review?.review && (!Number.isFinite(Date.parse(review.review.expires_at)) || Date.parse(review.review.expires_at) <= Date.now()))
  const resetReview = () => { setReview(null); setConfirmed(false); setError(''); idempotency.current = '' }
  const update = (next: ManagedPlatformDefaults) => { setDrafts(current => next.kind === 'supabase' ? { ...current, supabase: next } : { ...current, neon: next }); resetReview() }
  const chooseKind = (value: string) => { const next = catalog.items.find(item => item.kind === value); if (!next) return; setKind(next.kind); resetReview() }
  const toggleNode = (name: string) => { setNodesByKind(current => { const values = current[kind]; return { ...current, [kind]: values.includes(name) ? values.filter(value => value !== name) : values.length < entry.maximum_nodes ? [...values, name] : values } }); resetReview() }
  const chooseSecret = (key: string, value: string) => { setSecretsByKind(current => ({ ...current, [kind]: { ...current[kind], [key]: value } })); resetReview() }
  const spec = completeSpec(draft, nodes, entry.required_secret_keys, secrets, catalog.secret_references)
  async function submit() {
    if (nodes.length < entry.minimum_nodes || nodes.length > entry.maximum_nodes) { setError(`Choose ${entry.minimum_nodes === entry.maximum_nodes ? entry.minimum_nodes : `${entry.minimum_nodes} to ${entry.maximum_nodes}`} configured node${entry.maximum_nodes === 1 ? '' : 's'} before review.`); return }
    if (!spec) { setError('Choose every required node and secret reference before review.'); return }
    setBusy(true); setError('')
    const intent = { project: catalog.project, environment: catalog.environment, expected_revision: 0, kind: 'create' as const, spec }
    try {
      if (!review || expired || Boolean(review.review && Date.parse(review.review.expires_at) <= Date.now())) { setReview(await unwrap(client.POST('/managed-platforms/reviews', { body: intent }))); setConfirmed(false); return }
      if (!review.review || review.blocked) throw new Error('The server did not authorize this platform plan.')
      if (!confirmed) throw new Error('Confirm the reviewed plan before creating the platform.')
      if (!idempotency.current) idempotency.current = crypto.randomUUID()
      const operation = await unwrap(client.POST('/managed-platforms/operations', { params: { header: { 'Idempotency-Key': idempotency.current } }, body: { ...intent, id: review.platform.id, review: review.review } }))
      void cache.invalidateQueries({ queryKey: ['managed-platforms'] })
      await navigate({ to: '/platforms/$platformId', params: { platformId: operation.platform_id }, search: { project: catalog.project, environment: catalog.environment } })
    } catch (value) { setError(message(value)) } finally { setBusy(false) }
  }
  return <FormPage title="New platform" description="Configure an owned platform, then review the exact server plan before scheduling it." breadcrumbs={[]}>
    <form onSubmit={event => { event.preventDefault(); if (!busy) void submit() }}>
      <FormSection title="Platform">
        <div className="grid gap-4 sm:grid-cols-2">
          <label>Name<Input required maxLength={40} pattern="[a-z][a-z0-9-]*" value={draft.name} disabled={busy} onChange={event => update({ ...draft, name: event.target.value })}/><span className="field-help">Lowercase letters, digits and hyphens; begin with a letter.</span></label>
          <label>Platform<SelectField required label="Platform" title={`${managedPlatformName(entry.kind)} ${entry.version}`} value={kind} disabled={busy} onValueChange={chooseKind} options={catalog.items.map(item => ({ value: item.kind, label: `${managedPlatformName(item.kind)} ${item.kind === 'neon' ? item.version.slice(0, 12) : item.version}` }))}/></label>
        </div>
        <div className="mt-4"><Note>{entry.capability.reason}</Note></div>
      </FormSection>
      <PlatformSettings draft={draft} busy={busy} update={update}/>
      <FormSection title="Placement" description="Configured nodes are approved capacity references. Their presence here does not assert current health or physical-zone qualification.">
        {!catalog.nodes.length ? <Note>No approved capacity nodes are configured for this environment.</Note> : <div className="grid gap-2 sm:grid-cols-2">{catalog.nodes.map(node => <label key={node.uid} className="flex min-w-0 items-start gap-2 rounded border border-border p-3"><input type="checkbox" className="mt-1" checked={nodes.includes(node.name)} disabled={busy || (!nodes.includes(node.name) && nodes.length >= entry.maximum_nodes)} onChange={() => toggleNode(node.name)}/><span className="min-w-0 break-all">{node.name}<span className="block text-xs text-muted-foreground">Configured node reference</span></span></label>)}</div>}
        <p className="field-help mt-3">Choose {entry.minimum_nodes === entry.maximum_nodes ? entry.minimum_nodes : `${entry.minimum_nodes} to ${entry.maximum_nodes}`} node{entry.maximum_nodes === 1 ? '' : 's'}.</p>
        {draft.kind === 'neon' && <div className="mt-4 max-w-sm"><label>Member separation<SelectField required label="Member separation" value={draft.placement.spread || ''} disabled={busy} options={[{ value: 'nodes', label: 'One member per node' }, { value: 'zones', label: 'One member per zone' }]} onValueChange={spread => update({ ...draft, placement: { ...draft.placement, spread: spread as 'nodes' | 'zones' } })}/></label></div>}
      </FormSection>
      <FormSection title="Secret references" description="Choose configured immutable references. Secret values are never shown or entered here.">
        {!catalog.secret_references.length ? <Note>No immutable secret references are configured for this scope.</Note> : <div className="grid gap-4 sm:grid-cols-2">{entry.required_secret_keys.map(key => <label key={key}>{key}<SelectField required label={key} value={secrets[key] || ''} disabled={busy} onValueChange={value => chooseSecret(key, value)} options={[{ value: '', label: 'Choose a secret reference' }, ...catalog.secret_references.map(reference => ({ value: `${reference.name}@${reference.revision}`, label: `${reference.name} · revision ${reference.revision}` }))]}/></label>)}</div>}
      </FormSection>
      <ResourceFields draft={draft} busy={busy} update={update}/>
      {review && <FormSection title="Review"><div className="grid gap-3 text-sm">
        <p>{catalog.project} / {catalog.environment} · {managedPlatformName(draft.kind)} · {draft.name}</p>
        <p>Namespace: <span className="break-all">{review.plan.namespace}</span></p>
        <p>{review.plan.components.length} planned components · Storage class: {review.plan.storage_class} · public service {review.plan.public_service || 'none'}</p>
        <div className="flex flex-wrap items-center gap-2"><Status value={review.plan.capability.available ? 'available' : 'blocked'}/><span>{review.plan.capability.reason}</span></div>
        {review.review && <p>Review expires {timestamp(review.review.expires_at)}.{expired ? ' Refresh the review before creating.' : ''}</p>}
        {review.review?.blocked_reasons.map(reason => <Note key={reason}>{reason}</Note>)}
        {review.blocked && !review.review?.blocked_reasons.length && <Note>The server capability gate blocked creation.</Note>}
        {!review.blocked && review.review && <label className="flex items-start gap-2"><input type="checkbox" className="mt-1" checked={confirmed} disabled={busy || expired} onChange={event => setConfirmed(event.target.checked)}/><span>I reviewed this desired plan. Creating it schedules an operation; it does not prove the platform is ready.</span></label>}
        <Button type="button" disabled={busy} onClick={resetReview}>Edit or refresh review</Button>
      </div></FormSection>}
      {error && <FormError>{error}</FormError>}
      <div className="form-footer"><Button asChild><Link to="/platforms" search={{ project: catalog.project, environment: catalog.environment }}>Cancel</Link></Button><Button type="submit" variant="primary" disabled={busy || nodes.length < entry.minimum_nodes || nodes.length > entry.maximum_nodes || Boolean(review && (review.blocked || !review.review || expired || !confirmed))}>{busy ? 'Working…' : review ? 'Create platform' : 'Review platform'}</Button></div>
    </form>
  </FormPage>
}

function completeSpec(draft: ManagedPlatformDefaults, nodes: string[], keys: string[], selected: Record<string, string>, references: ManagedPlatformCatalog['secret_references']): ManagedPlatformSpec | null {
  const secrets: Record<string, { name: string; revision: number }> = {}
  for (const key of keys) { const value = selected[key]; const reference = references.find(item => `${item.name}@${item.revision}` === value); if (!reference) return null; secrets[key] = reference }
  if (draft.kind === 'supabase') return { ...draft, secrets, placement: { node_names: nodes, spread: '' } }
  return { ...draft, secrets, placement: { node_names: nodes, spread: draft.placement.spread } }
}

function PlatformSettings({ draft, busy, update }: { draft: ManagedPlatformDefaults; busy: boolean; update: (value: ManagedPlatformDefaults) => void }) {
  if (draft.kind === 'supabase') { const config = draft.supabase; const set = (fields: Partial<typeof config>) => update({ ...draft, supabase: { ...config, ...fields } }); return <FormSection title="Supabase settings"><div className="grid gap-4 sm:grid-cols-2"><label>Public HTTPS origin<Input required type="url" value={config.public_url} disabled={busy} onChange={e => set({ public_url: e.target.value })}/></label><label>Site HTTPS origin<Input required type="url" value={config.site_url} disabled={busy} onChange={e => set({ site_url: e.target.value })}/></label><label>Allowed redirect origins<Input value={(config.redirect_urls || []).join(', ')} disabled={busy} onChange={e => set({ redirect_urls: e.target.value.split(',').map(v => v.trim()).filter(Boolean) })}/><span className="field-help">Comma-separated exact HTTPS origins.</span></label><label>Database name<Input required value={config.database_name} disabled={busy} onChange={e => set({ database_name: e.target.value })}/></label><NumberField label="JWT expiry (seconds)" value={config.jwt_expiry_seconds} min={300} max={86400} busy={busy} set={value => set({ jwt_expiry_seconds: value })}/><NumberField label="REST maximum rows" value={config.rest_max_rows} min={1} max={10000} busy={busy} set={value => set({ rest_max_rows: value })}/><NumberField label="Storage file limit (bytes)" value={config.storage_file_limit_bytes} min={1048576} max={5368709120} busy={busy} set={value => set({ storage_file_limit_bytes: value })}/><NumberField label="Pool size" value={config.pool_size} min={1} max={100} busy={busy} set={value => set({ pool_size: value })}/><NumberField label="Maximum pool clients" value={config.pool_max_clients} min={config.pool_size} max={1000} busy={busy} set={value => set({ pool_max_clients: value })}/><label className="flex items-start gap-2 sm:col-span-2"><input type="checkbox" className="mt-1" checked={Boolean(config.anonymous_signup)} disabled={busy} onChange={e => set({ anonymous_signup: e.target.checked })}/><span>Allow anonymous signup. Email signup remains unavailable.</span></label></div></FormSection> }
  const config = draft.neon; const set = (fields: Partial<typeof config>) => update({ ...draft, neon: { ...config, ...fields } }); return <FormSection title="Neon settings"><div className="grid gap-4 sm:grid-cols-2"><NumberField label="Compute replicas" value={config.compute_replicas} min={1} max={6} busy={busy} set={value => set({ compute_replicas: value })}/><NumberField label="Pageservers" value={config.pageservers} min={2} max={8} busy={busy} set={value => set({ pageservers: value })}/><NumberField label="Branch limit" value={config.branch_limit} min={1} max={64} busy={busy} set={value => set({ branch_limit: value })}/><label>Object storage HTTPS origin<Input required type="url" value={config.object_storage_url} disabled={busy} onChange={e => set({ object_storage_url: e.target.value })}/></label><label>Bucket<Input required value={config.object_storage_bucket} disabled={busy} onChange={e => set({ object_storage_bucket: e.target.value })}/></label><label>Region<Input required value={config.object_storage_region} disabled={busy} onChange={e => set({ object_storage_region: e.target.value })}/></label><label>Resource prefix<Input required value={config.object_storage_prefix} disabled={busy} onChange={e => set({ object_storage_prefix: e.target.value })}/></label></div></FormSection>
}

function ResourceFields({ draft, busy, update }: { draft: ManagedPlatformDefaults; busy: boolean; update: (value: ManagedPlatformDefaults) => void }) { return <FormSection title="Resources and storage" description="Server defaults are editable desired requests. Expand this section only when the workload needs a different allocation."><details><summary>Customize component resources and storage</summary><div className="mt-4 grid gap-4 sm:grid-cols-2">{Object.entries(draft.resources).map(([name, resource]) => <div className="grid grid-cols-2 gap-2" key={name}><label>{name} CPU<Input required value={resource.cpu} disabled={busy} onChange={e => update({ ...draft, resources: { ...draft.resources, [name]: { ...resource, cpu: e.target.value } } })}/></label><label>{name} memory<Input required value={resource.memory} disabled={busy} onChange={e => update({ ...draft, resources: { ...draft.resources, [name]: { ...resource, memory: e.target.value } } })}/></label></div>)}{Object.entries(draft.storage).map(([name, value]) => <label key={name}>{name} storage (GiB)<Input required type="number" min={1} max={1024} value={value} disabled={busy} onChange={e => update({ ...draft, storage: { ...draft.storage, [name]: Number(e.target.value) } })}/></label>)}</div></details></FormSection> }

function NumberField({ label, value, min, max, busy, set }: { label: string; value: number; min: number; max: number; busy: boolean; set: (value: number) => void }) { return <label>{label}<Input required type="number" min={min} max={max} value={value} disabled={busy} onChange={event => set(Number(event.target.value))}/></label> }
