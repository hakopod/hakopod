import { useEffect, useRef, useState, type ReactNode } from 'react'
import { createFileRoute, Link, useNavigate } from '@tanstack/react-router'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { client, unwrap } from '../lib/client'
import type { components } from '../lib/api.generated'
import { databaseHealth, oracleSwitchoverNeedsFreshReview, useDatabase, useDatabaseOperations } from '../lib/databases'
import { canAccess, useResourceScope, useScope } from '../lib/scope'
import { message, timestamp } from '../lib/api'
import { FormError, FormPage, FormSection } from '../components/form-page'
import { Empty, ErrorState, Loading, Note } from '../components/shared'
import { Button } from '../components/ui/button'
import { Input } from '../components/ui/input'
import { SelectField } from '../components/ui/select'

type SwitchoverPlan = components['schemas']['DatabaseOracleSwitchoverPlan']
export const Route = createFileRoute('/databases/$databaseId/switchover')({
  validateSearch: (search: Record<string, unknown>): { operation?: string } => ({
    operation: typeof search.operation === 'string' && /^[a-f0-9]{32}$/.test(search.operation) ? search.operation : undefined,
  }),
  component: Page,
})
function Page() {
  const { databaseId } = Route.useParams()
  const { operation } = Route.useSearch()
  return <Switchover key={`${databaseId}:${operation || 'new'}`} id={databaseId} operationId={operation} />
}
function Switchover({ id, operationId }: { id: string; operationId?: string }) {
  const query = useDatabase(id)
  const history = useDatabaseOperations(id, !operationId && query.data?.status === 'failed')
  useResourceScope(query.data)
  const operation = useQuery({
    queryKey: ['database-operation', operationId],
    queryFn: ({ signal }) => unwrap(client.GET('/database-operations/{id}', { signal, params: { path: { id: operationId! } } })),
    enabled: Boolean(operationId), refetchInterval: 5000, gcTime: 0,
  })
  const { identity } = useScope()
  const [target, setTarget] = useState('')
  const [plan, setPlan] = useState<SwitchoverPlan | null>(null)
  const [confirmation, setConfirmation] = useState('')
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  const [tick, setTick] = useState(Date.now)
  const now = Math.max(tick, Date.now())
  const key = useRef('')
  const navigate = useNavigate()
  const cache = useQueryClient()
  useEffect(() => { const timer = setInterval(() => setTick(Date.now()), 1000); return () => clearInterval(timer) }, [])
  const frame = (children: ReactNode) => <FormPage title={operationId ? 'Resume switchover' : 'Switch Oracle primary'} description="Review a healthy standby before moving the primary role. Existing connections will close." breadcrumbs={[]}>{children}</FormPage>
  if (!query.data) return frame(query.error ? <ErrorState error={query.error} /> : <Loading />)
  const d = query.data
  const search = { project: d.project, environment: d.environment }
  const back = <Button asChild disabled={busy}><Link to="/databases/$databaseId" params={{ databaseId: id }} search={{ ...search, tab: 'activity' }}>Back to database</Link></Button>
  if (identity.application || !canAccess(identity, d.project, 'deployments:write')) return frame(<Note>Switching the primary requires project deployment permission.</Note>)
  if (d.spec.engine !== 'oracle' || d.spec.oracle?.edition !== 'enterprise' || d.spec.mode !== 'cluster') return frame(<Empty title="Oracle Enterprise cluster required" description="A graceful Data Guard switchover needs an Enterprise primary and a healthy physical standby." action={back} />)
  if (operationId && !operation.data) return frame(operation.error ? <ErrorState error={operation.error} /> : <Loading />)
  const retry = operationId ? operation.data : undefined
  if (retry && (retry.database_id !== id || retry.kind !== 'switchover' || !retry.switchover)) return frame(<Note>This operation does not belong to this database's switchover history.</Note>)
  const selectedPlan = retry?.switchover || plan?.plan
  const standbys = d.observation.members.filter((member) => member.ready && member.role === 'replica')
  const freshReview = !history.error && oracleSwitchoverNeedsFreshReview(d, history.data?.items[0])
  const healthy = !query.error && databaseHealth(d, now) === 'ready' && (d.status === 'ready' || freshReview) && (!d.recovery || Boolean(d.recovery.restored_at && d.recovery.inspected_at))
  const changed = Boolean(selectedPlan && (selectedPlan.database_id !== id || selectedPlan.revision !== d.revision || selectedPlan.project !== d.project || selectedPlan.environment !== d.environment))
  const expired = Boolean(plan && (!Number.isFinite(Date.parse(plan.plan.expires_at)) || Date.parse(plan.plan.expires_at) <= now))
  const retryable = Boolean(retry && retry.status === 'failed' && d.status === 'failed' && !['review', 'switchover'].includes(retry.phase))
  const blocked = busy || Boolean(query.error || operation.error) || changed || (retry ? !retryable : !healthy || expired)
  return frame(<form className="grid gap-4" onSubmit={async (event) => {
    event.preventDefault()
    if (blocked) return
    setError(''); setBusy(true)
    try {
      if (!plan && !retry) {
        const reviewed = await unwrap(client.POST('/databases/{id}/switchover-plan', { params: { path: { id } }, body: { target_member: target } }))
        if (reviewed.plan.database_id !== id || reviewed.plan.project !== d.project || reviewed.plan.environment !== d.environment || reviewed.plan.revision !== d.revision || reviewed.plan.target !== target) throw new Error('The returned review does not match this database and standby. Review again.')
        setPlan(reviewed); setConfirmation(''); key.current = ''; return
      }
      if (confirmation !== d.spec.name) return
      if (!key.current) key.current = crypto.randomUUID()
      const accepted = retry
        ? await unwrap(client.POST('/databases/{id}/switchover-retry', { params: { path: { id } }, body: { operation_id: retry.id, expected_revision: retry.revision, confirm_name: confirmation } }))
        : await unwrap(client.POST('/databases/{id}/switchover', { params: { path: { id }, header: { 'Idempotency-Key': key.current } }, body: { review_id: plan!.id, expected_revision: plan!.plan.revision, confirm_name: confirmation } }))
      if (accepted.database_id !== id || accepted.kind !== 'switchover' || (retry && accepted.id !== retry.id)) throw new Error('The API returned a different operation. Refresh the operation history before retrying.')
      await cache.invalidateQueries({ queryKey: ['managed-database', id] })
      await cache.invalidateQueries({ queryKey: ['database-operations', id] })
      void navigate({ to: '/databases/$databaseId', params: { databaseId: id }, search: { ...search, tab: 'activity' } })
    } catch (err) { setError(message(err)) } finally { setBusy(false) }
  }}>
    {query.error && <FormError focus={false}>Database status could not be refreshed. Your selections are preserved. Refresh before continuing.</FormError>}
    {operation.error && <FormError focus={false}>The operation could not be refreshed. Your confirmation is preserved.</FormError>}
    {history.error && <FormError focus={false}>The operation history could not be refreshed. Your selections are preserved.</FormError>}
    {freshReview && <Note>The previous review became stale before the role change started. Choose a healthy standby for a new review.</Note>}
    {!retry && !healthy && <Note>Wait for a current, healthy primary and all standby members before reviewing a switchover.</Note>}
    {retry && <Note>Resume the same reviewed target and request. This does not force a failover or choose another primary. The person who started the operation must resume it.</Note>}
    {retry && !retryable && <Note>{retry.phase === 'review' ? <div className="grid justify-items-start gap-2"><span>The previous review became stale before the role change started.</span><Button asChild><Link to="/databases/$databaseId/switchover" params={{ databaseId: id }} search={search}>Review current topology</Link></Button></div> : retry.phase === 'switchover' ? 'The broker reported a failed switchover. Inspect native recovery before changing roles again.' : 'This operation is not awaiting a retry. Check its current status in Activity.'}</Note>}
    <FormSection title={selectedPlan ? 'Reviewed role change' : 'Choose standby'}>
      <dl className="grid gap-3 text-sm sm:grid-cols-2"><div><dt className="text-muted-foreground">Database</dt><dd>{d.spec.name}</dd></div><div><dt className="text-muted-foreground">{selectedPlan ? 'Reviewed primary' : 'Current primary'}</dt><dd className="break-all">{selectedPlan?.primary || d.observation.primary || 'Unavailable'}</dd></div></dl>
      {!selectedPlan ? <SelectField label="New primary" value={target} disabled={blocked} required onValueChange={setTarget} options={[{ value: '', label: 'Choose a healthy standby' }, ...standbys.map((member) => ({ value: member.name, label: `${member.name}${member.zone ? ` · ${member.zone}` : ''}` }))]} /> : <dl className="grid gap-3 text-sm sm:grid-cols-2"><div><dt className="text-muted-foreground">Reviewed target</dt><dd className="break-all">{selectedPlan.target}</dd></div><div><dt className="text-muted-foreground">Configuration revision</dt><dd>{selectedPlan.revision}</dd></div>{!retry && <div><dt className="text-muted-foreground">Review expires</dt><dd>{timestamp(selectedPlan.expires_at)}</dd></div>}</dl>}
      {Boolean(plan?.warnings.length) && <Note><ul className="grid list-disc gap-2 pl-4">{plan!.warnings.map((warning) => <li key={warning}>{warning}</li>)}</ul></Note>}
      {retry?.message && <Note>{retry.message}</Note>}
      {changed && <Note>The database revision or scope changed. Return to the database and review its current topology.</Note>}
      {expired && <Note>This review expired. Review the current topology again.</Note>}
      {selectedPlan && <label htmlFor="switchover-confirm" className="grid gap-2">Type {d.spec.name} to confirm<Input id="switchover-confirm" autoComplete="off" required value={confirmation} disabled={busy} onChange={(event) => setConfirmation(event.target.value)} /></label>}
    </FormSection>
    {error && <FormError>{error}</FormError>}
    <div className="form-actions flex flex-wrap gap-2">{back}{plan && <Button type="button" disabled={busy} onClick={() => { setPlan(null); setConfirmation(''); key.current = ''; setError('') }}>Review again</Button>}<Button type="submit" variant="primary" disabled={blocked || (selectedPlan ? confirmation !== d.spec.name : !target)}>{busy ? 'Checking…' : retry ? 'Resume switchover' : plan ? 'Switch primary' : 'Review switchover'}</Button></div>
  </form>)
}
