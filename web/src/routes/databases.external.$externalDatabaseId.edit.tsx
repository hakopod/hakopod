import { useRef, useState } from 'react'
import { createFileRoute, Link, useNavigate } from '@tanstack/react-router'
import { useQueryClient } from '@tanstack/react-query'
import { useExternalDatabase } from '../lib/external-databases'
import { canAccess, useResourceScope, useScope } from '../lib/scope'
import { client, unwrap } from '../lib/client'
import { message } from '../lib/api'
import { ErrorState, Loading, Note } from '../components/shared'
import { FormError, FormPage, FormSection } from '../components/form-page'
import { Button } from '../components/ui/button'
import { Input } from '../components/ui/input'

export const Route = createFileRoute('/databases/external/$externalDatabaseId/edit')({ component: Page })
function Page() {
  const { externalDatabaseId } = Route.useParams()
  const query = useExternalDatabase(externalDatabaseId)
  useResourceScope(query.data)
  const { identity } = useScope()
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [confirmation, setConfirmation] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const key = useRef('')
  const cache = useQueryClient()
  const navigate = useNavigate()
  if (query.isPending) return <Loading />
  if (query.error && !query.data) return <ErrorState error={query.error} />
  const database = query.data!
  if (identity.application || !canAccess(identity, database.project, 'deployments:write')) return <Note>Credential rotation requires project deployment permission.</Note>
  return <FormPage title="Rotate credentials" description="Replace the saved credential for this legacy external connection without changing its endpoint." breadcrumbs={[]}>
    {query.error && <Note>Connection refresh failed. Your entries are preserved; wait for a successful refresh before submitting.</Note>}
    <form className="grid gap-4" onSubmit={async (event) => {
      event.preventDefault()
      if (busy || query.error || confirmation !== database.spec.name) return
      setBusy(true)
      setError('')
      try {
        if (!key.current) key.current = crypto.randomUUID()
        await unwrap(client.PUT('/external-databases/{id}', {
          params: { path: { id: database.id }, header: { 'Idempotency-Key': key.current } },
          body: { spec: database.spec, credentials: { username, password }, expected_revision: database.revision, confirm_name: confirmation },
        }))
        await cache.invalidateQueries({ queryKey: ['external-database', database.id] })
        void navigate({ to: '/databases/external/$externalDatabaseId', params: { externalDatabaseId: database.id }, search: { project: database.project, environment: database.environment } })
      } catch (err) {
        setError(message(err))
      } finally {
        setBusy(false)
      }
    }}>
      <FormSection title="New provider credential">
        <div className="grid gap-4">
          <label htmlFor="external-username">Username<Input id="external-username" required autoComplete="username" maxLength={256} value={username} disabled={busy} onChange={(event) => { setUsername(event.target.value); key.current = ''; setError('') }} /></label>
          <label htmlFor="external-password">Password<Input id="external-password" required type="password" autoComplete="new-password" maxLength={4096} value={password} disabled={busy} onChange={(event) => { setPassword(event.target.value); key.current = ''; setError('') }} /></label>
          <label htmlFor="external-confirm">Type {database.spec.name} to confirm<Input id="external-confirm" required autoComplete="off" value={confirmation} disabled={busy} onChange={(event) => { setConfirmation(event.target.value); key.current = ''; setError('') }} /></label>
        </div>
        <Note>Existing application bindings stay pinned to their current revision. Rotate or revoke credentials at the provider only after you have accounted for every running application.</Note>
      </FormSection>
      {error && <FormError>{error}</FormError>}
      <div className="form-actions flex flex-wrap gap-2"><Button asChild disabled={busy}><Link to="/databases/external/$externalDatabaseId" params={{ externalDatabaseId: database.id }} search={{ project: database.project, environment: database.environment }}>Cancel</Link></Button><Button type="submit" variant="primary" disabled={busy || Boolean(query.error) || !username || !password || confirmation !== database.spec.name}>{busy ? 'Rotating…' : 'Rotate credentials'}</Button></div>
    </form>
  </FormPage>
}
