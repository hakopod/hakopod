import { useEffect, useRef, useState, type ReactNode } from 'react'
import { createFileRoute, Link, useNavigate } from '@tanstack/react-router'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { client, unwrap } from '../lib/client'
import type { components } from '../lib/api.generated'
import { useDatabase } from '../lib/databases'
import { canAccess, useResourceScope, useScope } from '../lib/scope'
import { message, timestamp } from '../lib/api'
import { FormError, FormPage, FormSection } from '../components/form-page'
import { ErrorState, Loading, Note } from '../components/shared'
import { Button } from '../components/ui/button'
import { Input } from '../components/ui/input'

type RetryPlan = { id: string; plan: components['schemas']['DatabaseResizeRetryReview'] }
export const Route = createFileRoute('/databases/$databaseId/resize-retry')({
  validateSearch: (search: Record<string, unknown>): { operation?: string } => ({
    operation: typeof search.operation === 'string' && /^[a-f0-9]{32}$/.test(search.operation) ? search.operation : undefined,
  }),
  component: Page,
})
function Page() {
  const { databaseId } = Route.useParams()
  const { operation } = Route.useSearch()
  return <ReplicaRetry key={`${databaseId}:${operation || ''}`} id={databaseId} operationId={operation} />
}
function ReplicaRetry({ id, operationId }: { id: string; operationId?: string }) {
  const query = useDatabase(id)
  useResourceScope(query.data)
  const operation = useQuery({
    queryKey: ['database-operation', operationId],
    queryFn: ({ signal }) => unwrap(client.GET('/database-operations/{id}', { signal, params: { path: { id: operationId! } } })),
    enabled: Boolean(operationId), refetchInterval: 5000, gcTime: 0,
  })
  const { identity } = useScope()
  const [review, setReview] = useState<RetryPlan | null>(null)
  const [confirmation, setConfirmation] = useState('')
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  const [tick, setTick] = useState(Date.now)
  const requestKey = useRef('')
  const navigate = useNavigate()
  const cache = useQueryClient()
  useEffect(() => { const timer = setInterval(() => setTick(Date.now()), 1000); return () => clearInterval(timer) }, [])
  const frame = (children: ReactNode) => <FormPage title="Retry replica change" description="Check the current database before resuming the same requested layout." breadcrumbs={[]}>{children}</FormPage>
  if (!query.data) return frame(query.error ? <ErrorState error={query.error} /> : <Loading />)
  const d = query.data
  const search = { project: d.project, environment: d.environment }
  const back = <Button asChild disabled={busy}><Link to="/databases/$databaseId" params={{ databaseId: id }} search={{ ...search, tab: 'activity' }}>Back to database</Link></Button>
  if (identity.application || !canAccess(identity, d.project, 'deployments:write')) return frame(<><Note>Retrying a replica change requires project deployment permission.</Note>{back}</>)
  if (!operationId) return frame(<><Note>Select a failed replica change from the database's Activity tab.</Note>{back}</>)
  if (!operation.data) return frame(operation.error ? <ErrorState error={operation.error} /> : <Loading />)
  const op = operation.data
  const supported = ['mysql', 'mongodb'].includes(d.spec.engine) && d.spec.mode === 'cluster'
  if (!supported || op.database_id !== id || !['resize', 'resize-retry'].includes(op.kind) || op.revision !== d.revision) return frame(<><Note>This operation does not match the database's current replica change.</Note>{back}</>)
  const plan = review?.plan
  const retryable = d.status === 'failed' && op.status === 'failed'
  const expired = Boolean(plan && (!Number.isFinite(Date.parse(plan.expires_at)) || Date.parse(plan.expires_at) <= Math.max(tick, Date.now())))
  const changed = Boolean(plan && (plan.database_id !== id || plan.operation_id !== op.id || plan.revision !== d.revision))
  const blocked = busy || !retryable || Boolean(query.error || operation.error || plan?.resize.blocked_reasons.length) || expired || changed
  return frame(<form className="grid gap-4" onSubmit={async (event) => {
    event.preventDefault()
    if (blocked) return
    setError(''); setBusy(true)
    try {
      if (!review) {
        const result = await unwrap(client.POST('/databases/{id}/resize-retry-plan', { params: { path: { id } }, body: { operation_id: op.id, expected_revision: d.revision } }))
        if (result.plan.database_id !== id || result.plan.operation_id !== op.id || result.plan.revision !== d.revision || !['accepted', 'prior'].includes(result.plan.state)) throw new Error('The returned review does not match this replica change. Review again.')
        setReview(result); setConfirmation(''); requestKey.current = ''; return
      }
      if (confirmation !== d.spec.name) return
      if (!requestKey.current) requestKey.current = crypto.randomUUID()
      const accepted = await unwrap(client.POST('/databases/{id}/resize-retry', { params: { path: { id }, header: { 'Idempotency-Key': requestKey.current } }, body: { operation_id: op.id, expected_revision: review.plan.revision, review_id: review.id, confirm_name: confirmation } }))
      if (accepted.database_id !== id || accepted.revision !== review.plan.revision || accepted.kind !== 'resize-retry' || accepted.id === op.id) throw new Error('The API returned a different replica operation. Refresh Activity before continuing.')
      await cache.invalidateQueries({ queryKey: ['managed-database', id] })
      await cache.invalidateQueries({ queryKey: ['database-operations', id] })
      void navigate({ to: '/databases/$databaseId', params: { databaseId: id }, search: { ...search, tab: 'activity' } })
    } catch (err) { setError(message(err)) } finally { setBusy(false) }
  }}>
    {(query.error || operation.error) && <FormError focus={false}>The latest state could not be loaded. Your review and confirmation are preserved. Refresh before continuing.</FormError>}
    {!retryable && <Note>This operation is no longer awaiting a retry. Check its current status in Activity.</Note>}
    <FormSection title="Requested layout">
      <dl className="grid gap-3 text-sm sm:grid-cols-2"><div><dt className="text-muted-foreground">Database</dt><dd>{d.spec.name}</dd></div><div><dt className="text-muted-foreground">Configuration revision</dt><dd>{d.revision}</dd></div><div><dt className="text-muted-foreground">Requested voting members</dt><dd>{d.spec.replicas + 1}</dd></div><div><dt className="text-muted-foreground">Previous attempt</dt><dd>{op.phase}</dd></div></dl>
      {op.message && <Note>{op.message}</Note>}
      <p className="text-sm text-muted-foreground">The retry keeps the requested replica count and existing reservations. It cannot choose a different layout or discard data. The database stays unready until all native checks pass.</p>
    </FormSection>
    {plan && <FormSection title="Review retry">
      <dl className="grid gap-3 text-sm sm:grid-cols-2"><div><dt className="text-muted-foreground">Controller state</dt><dd>{plan.state === 'accepted' ? 'Requested revision accepted; convergence will be checked again.' : 'Previous layout verified; the requested change has not started.'}</dd></div><div><dt className="text-muted-foreground">Review expires</dt><dd>{timestamp(plan.expires_at)}</dd></div></dl>
      {plan.resize.warnings.length > 0 && <Note><ul className="grid list-disc gap-2 pl-4">{plan.resize.warnings.map((warning) => <li key={warning}>{warning}</li>)}</ul></Note>}
      {plan.resize.blocked_reasons.length > 0 && <Note><ul className="grid list-disc gap-2 pl-4">{plan.resize.blocked_reasons.map((reason) => <li key={reason}>{reason}</li>)}</ul></Note>}
      <label htmlFor="replica-retry-confirm" className="grid gap-2">Type {d.spec.name} to confirm<Input id="replica-retry-confirm" autoComplete="off" required value={confirmation} disabled={busy} onChange={(event) => setConfirmation(event.target.value)} /></label>
    </FormSection>}
    {changed && <Note>The database revision changed. Return to Activity and review the latest operation.</Note>}
    {expired && <Note>This review expired. Check the current database again before retrying.</Note>}
    {error && <FormError>{error}</FormError>}
    <div className="form-actions flex flex-wrap gap-2">{back}{review && <Button type="button" disabled={busy} onClick={() => { setReview(null); setConfirmation(''); setError(''); requestKey.current = '' }}>Review again</Button>}<Button type="submit" variant="primary" disabled={blocked || Boolean(review && confirmation !== d.spec.name)}>{busy ? 'Checking…' : review ? 'Retry replica change' : 'Review retry'}</Button></div>
  </form>)
}
