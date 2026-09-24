import { useState } from 'react'
import { createFileRoute } from '@tanstack/react-router'
import { useQuery } from '@tanstack/react-query'
import { client, unwrap } from '../lib/client'
import { message, timestamp } from '../lib/api'
import { SelectField } from '../components/ui/select'
import { dashboardEdition } from '../lib/dashboard-edition'
import { Button } from '../components/ui/button'
import { Empty, ErrorState, Loading, Note, PageHeader, RequestError } from '../components/shared'

export const Route = createFileRoute('/login/device')({
  validateSearch: (search: Record<string, unknown>) => ({
    user_code: typeof search.user_code === 'string' ? search.user_code.slice(0, 64) : '',
  }),
  component: DeviceConsent,
})
function DeviceConsent() {
  const { user_code } = Route.useSearch()
  const [selection, setSelection] = useState('')
  const [busy, setBusy] = useState(false)
  const [done, setDone] = useState<boolean | null>(null)
  const [error, setError] = useState('')
  const request = useQuery({
    queryKey: ['device-consent', user_code],
    queryFn: ({ signal }) =>
      unwrap(client.GET('/auth/device', { signal, params: { query: { user_code } } })),
    enabled: Boolean(user_code),
    retry: false,
    gcTime: 0,
  })
  const scopes =
    request.data?.scopes?.filter(
      (scope) =>
        !request.data?.project ||
        (scope.project === request.data.project && scope.environment === request.data.environment),
    ) || []
  const selected = scopes.find(
    (scope) => JSON.stringify([scope.id, scope.project, scope.environment]) === selection,
  )
  async function decide(approve: boolean) {
    if (busy) return
    setBusy(true)
    setError('')
    try {
      await unwrap(
        client.POST('/auth/device/approve', {
          body: {
            user_code,
            approve,
            ...(selected
              ? {
                  scope_id: selected.id,
                  project: selected.project,
                  environment: selected.environment,
                }
              : {}),
          },
        }),
      )
      setDone(approve)
    } catch (err) {
      setError(message(err))
    } finally {
      setBusy(false)
    }
  }
  return (
    <>
      <PageHeader
        eyebrow="ACCOUNT / CLI ACCESS"
        title="Connect your terminal"
        description="Review the access requested by your Hakopod CLI."
      />
      {done !== null ? (
        <Empty
          icon={done ? 'check' : 'lock'}
          title={done ? 'Terminal authorized' : 'Request denied'}
          description="You can return to your terminal and close this page."
        />
      ) : !user_code ? (
        <Empty
          title="Missing device code"
          description="Run hakopod login in your terminal and open its authorization link."
        />
      ) : request.isPending ? (
        <Loading />
      ) : request.error ? (
        <ErrorState error={request.error} />
      ) : (
        request.data && (
          <section className="panel service-summary-panel settings-narrow">
            <h2>Authorize this request?</h2>
            <dl className="service-definition-list">
              <div>
                <dt>Code</dt>
                <dd>
                  <code>{request.data.user_code}</code>
                </dd>
              </div>
              <div>
                <dt>Deployment scope</dt>
                <dd>
                  {selected
                    ? `${selected.project} / ${selected.environment}`
                    : request.data.project
                      ? `${request.data.project} / ${request.data.environment}`
                      : 'Choose below'}
                </dd>
              </div>
              {selected?.id && (
                <div>
                  <dt>Workspace</dt>
                  <dd className="break-words">{selected.label}</dd>
                </div>
              )}
              <div>
                <dt>Permissions</dt>
                <dd>{request.data.permissions.join(', ')}</dd>
              </div>
              <div>
                <dt>Expires</dt>
                <dd>{timestamp(request.data.expires_at)}</dd>
              </div>
            </dl>
            <SelectField
              label="Deployment destination"
              value={selection}
              onValueChange={setSelection}
              required
              disabled={busy}
              options={[
                { value: '', label: 'Choose a destination' },
                ...scopes.map((scope) => ({
                  value: JSON.stringify([scope.id, scope.project, scope.environment]),
                  label: scope.label,
                })),
              ]}
            />
            {scopes.length === 0 && (
              <Note>
                No deployment destination is ready. Set up a project
                {dashboardEdition.cloud ? ' and its compute' : ''}, then refresh this list.
              </Note>
            )}
            <div className="toolbar-actions">
              <Button asChild>
                <a href="/" target="_blank" rel="noopener noreferrer">
                  Open dashboard setup
                </a>
              </Button>
              <Button disabled={request.isFetching || busy} onClick={() => void request.refetch()}>
                Refresh destinations
              </Button>
            </div>
            <Note>Approve only when this code matches the terminal where you started sign-in.</Note>
            {error && <RequestError error={error} />}
            <div className="toolbar-actions">
              <Button disabled={busy} onClick={() => void decide(false)}>
                Deny
              </Button>
              <Button
                variant="primary"
                disabled={busy || !selected}
                onClick={() => void decide(true)}
              >
                {busy ? 'Submitting…' : 'Authorize CLI'}
              </Button>
            </div>
          </section>
        )
      )}
    </>
  )
}
