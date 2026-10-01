import { useEffect, useRef, useState } from 'react'
import { createFileRoute, Link, useNavigate } from '@tanstack/react-router'
import { useQueryClient } from '@tanstack/react-query'
import { useExternalDatabase, useExternalDatabaseConnections, externalDatabaseHealth, type ExternalDatabaseConnectionPlan } from '../lib/external-databases'
import { useResourceScope, useScope, canAccess } from '../lib/scope'
import { client, unwrap } from '../lib/client'
import { message, timestamp } from '../lib/api'
import { FormError, FormPage, FormSection } from '../components/form-page'
import { ErrorState, Loading, Note } from '../components/shared'
import { Button } from '../components/ui/button'
import { Input } from '../components/ui/input'
import { SelectField } from '../components/ui/select'

export const Route = createFileRoute('/databases/external/$externalDatabaseId/connect')({ component: Page })
function Page() { return <Connect key={Route.useParams().externalDatabaseId} id={Route.useParams().externalDatabaseId} /> }
function Connect({ id }: { id: string }) {
  const database = useExternalDatabase(id)
  const connections = useExternalDatabaseConnections(id)
  useResourceScope(database.data)
  const { identity } = useScope()
  const [applicationId, setApplicationId] = useState('')
  const [service, setService] = useState('')
  const [variable, setVariable] = useState('DATABASE_URL')
  const [disconnect, setDisconnect] = useState(false)
  const [confirmation, setConfirmation] = useState('')
  const [plan, setPlan] = useState<ExternalDatabaseConnectionPlan | null>(null)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [tick, setNow] = useState(Date.now)
  const now = Math.max(tick, Date.now())
  const key = useRef('')
  const review = useRef<HTMLDivElement>(null)
  const navigate = useNavigate()
  const cache = useQueryClient()
  const d = database.data
  useEffect(() => { const timer = setInterval(() => setNow(Date.now()), 5000); return () => clearInterval(timer) }, [])
  useEffect(() => { if (plan) review.current?.focus() }, [plan])
  function reset() { setPlan(null); setConfirmation(''); setError(''); key.current = '' }
  if (database.error && !database.data) return <ErrorState error={database.error} />
  if (database.isPending || connections.isPending) return <Loading />
  if (connections.error && !connections.data) return <ErrorState error={connections.error} />
  if (!d) return null
  if (identity.application || !canAccess(identity, d.project, 'deployments:write')) return <Note>Changing application access requires project deployment permission.</Note>
  const search = { project: d.project, environment: d.environment }
  const expired = Boolean(plan && (!Number.isFinite(Date.parse(plan.expires_at)) || Date.parse(plan.expires_at) <= now))
  const changed = Boolean(plan && (plan.database_revision !== d.revision || plan.credential_revision !== d.credential_revision))
  const ready = externalDatabaseHealth(d, now) === 'ready'
  const savedBindings = (connections.data?.items || []).filter((binding) => binding.saved_revision > 0)
  const selected = savedBindings.find((binding) => binding.application_id === applicationId && binding.service === service && binding.variable === variable)
  const refreshFailed = Boolean(database.error || connections.error)
  const invalid = refreshFailed || Boolean(plan && (expired || changed))
  return <FormPage title="Update application binding" description="Use rotated credentials for an existing legacy binding, or disconnect it." breadcrumbs={[]}>
    <form className="grid gap-4" onSubmit={async (event) => {
      event.preventDefault(); if (busy || invalid) return
      setBusy(true); setError('')
      try {
        if (!plan) {
          if (!selected || !disconnect && !ready) return
          setPlan(await unwrap(client.POST('/external-databases/{id}/connection-plan', { params: { path: { id } }, body: { application_id: applicationId, service, variable, disconnect } })))
        } else {
          if (confirmation !== plan.application_name) return
          if (!key.current) key.current = crypto.randomUUID()
          const deployment = await unwrap(client.POST('/external-databases/{id}/connect', { params: { path: { id }, header: { 'Idempotency-Key': key.current } }, body: { review_id: plan.id, confirm_application: confirmation } }))
          void cache.invalidateQueries({ queryKey: ['applications'] })
          void cache.invalidateQueries({ queryKey: ['application', plan.application_id] })
          void cache.invalidateQueries({ queryKey: ['external-database-connections', id] })
          void navigate({ to: '/deployments/$deploymentId', params: { deploymentId: deployment.id } })
        }
      } catch (err) { setError(message(err)) } finally { setBusy(false) }
    }}>
      {refreshFailed && <FormError focus={false}>Connection or application status could not be refreshed. Your selections are preserved; wait for a successful refresh before continuing.</FormError>}
      <FormSection title="Application binding"><p className="text-sm">{d.spec.name} · {d.project} / {d.environment}</p><div className="grid min-w-0 gap-4 sm:grid-cols-2">
        <label className="min-w-0">Action<SelectField className="w-full min-w-0" label="Binding action" value={disconnect ? 'disconnect' : 'refresh'} disabled={busy || Boolean(plan)} onValueChange={(value) => { reset(); setDisconnect(value === 'disconnect') }} options={[{ value: 'refresh', label: 'Use rotated credentials' }, { value: 'disconnect', label: 'Disconnect binding' }]} /></label>
        <label className="min-w-0">Saved binding<SelectField className="w-full min-w-0" label="Saved binding" required value={selected ? `${applicationId}/${service}/${variable}` : ''} disabled={busy || Boolean(plan)} onValueChange={(value) => { reset(); const [nextApplication, nextService, nextVariable] = value.split('/'); setApplicationId(nextApplication || ''); setService(nextService || ''); setVariable(nextVariable || '') }} options={[{ value: '', label: 'Choose a saved binding' }, ...savedBindings.map((binding) => ({ value: `${binding.application_id}/${binding.service}/${binding.variable}`, label: `${binding.application_display_name || binding.application_name} · ${binding.service} · ${binding.variable}` }))]} /></label>
      </div>
        {!savedBindings.length && <Note>This connection has no saved application bindings to refresh or disconnect.</Note>}
        {connections.data?.truncated && <Note>The connection list reached its limit, so some saved bindings may be absent. Use <code className="break-all">hakopod external-database refresh-review CONNECTION_ID --application-id APP_ID --service SERVICE --variable VARIABLE</code>, then apply the returned review with <code className="break-all">hakopod external-database refresh CONNECTION_ID --review-id REVIEW_ID --name APP_NAME</code>. Use the matching disconnect-review and disconnect commands to remove a binding.</Note>}
      </FormSection>
      {!disconnect && !ready && <Note>A current TLS and authenticated-query check must pass before an existing binding can use the rotated credential revision.</Note>}
      {plan && <div ref={review} tabIndex={-1} className="outline-offset-4 focus:outline-2 focus:outline-ring"><FormSection title="Review application deployment"><dl className="db-create-facts"><div><dt>Action</dt><dd>{plan.kind === 'disconnect' ? 'Remove binding' : 'Use rotated credentials'}</dd></div><div><dt>Application</dt><dd>{plan.application_name} · r{plan.application_revision}</dd></div><div><dt>Binding</dt><dd>{plan.service} / {plan.variable}</dd></div><div><dt>Connection</dt><dd>{plan.database_name} · r{plan.database_revision}</dd></div><div><dt>Credentials</dt><dd>Revision {plan.credential_revision}</dd></div><div><dt>Review expires</dt><dd>{timestamp(plan.expires_at)}</dd></div></dl>{plan.warnings.length > 0 && <Note><ul className="list-disc space-y-2 pl-4">{plan.warnings.map((warning) => <li key={warning}>{warning}</li>)}</ul></Note>}<label htmlFor="external-application-confirm">Type {plan.application_name} to confirm redeployment<Input id="external-application-confirm" required autoComplete="off" disabled={busy || invalid} value={confirmation} onChange={(event) => setConfirmation(event.target.value)} /></label>{(expired || changed) && <Note>{expired ? 'The review expired.' : 'The database connection changed.'} Review the current configuration again.</Note>}</FormSection></div>}
      {error && <FormError>{error}</FormError>}
      <div className="form-actions flex flex-wrap gap-2"><Button asChild disabled={busy}><Link to="/databases/external/$externalDatabaseId" params={{ externalDatabaseId: id }} search={{ ...search, tab: 'connections' }}>Cancel</Link></Button>{plan && <Button disabled={busy} onClick={reset}>Review again</Button>}<Button type="submit" variant="primary" disabled={busy || invalid || (plan ? confirmation !== plan.application_name : !selected || !disconnect && !ready)}>{busy ? 'Submitting…' : plan ? `${plan.kind === 'disconnect' ? 'Disconnect' : 'Refresh'} and redeploy` : `Review ${disconnect ? 'disconnection' : 'credential refresh'}`}</Button></div>
    </form>
  </FormPage>
}
