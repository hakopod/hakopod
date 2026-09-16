import { useState } from 'react'
import { createFileRoute } from '@tanstack/react-router'
import { useQuery } from '@tanstack/react-query'
import { client, unwrap } from '../lib/client'
import { message, timestamp } from '../lib/api'
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
  async function decide(approve: boolean) {
    if (busy) return
    setBusy(true)
    setError('')
    try {
      await unwrap(client.POST('/auth/device/approve', { body: { user_code, approve } }))
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
                <dt>Scope</dt>
                <dd>
                  {request.data.project} / {request.data.environment}
                </dd>
              </div>
              <div>
                <dt>Permissions</dt>
                <dd>{request.data.permissions.join(', ')}</dd>
              </div>
              <div>
                <dt>Expires</dt>
                <dd>{timestamp(request.data.expires_at)}</dd>
              </div>
            </dl>
            <Note>Approve only when this code matches the terminal where you started sign-in.</Note>
            {error && <RequestError error={error} />}
            <div className="toolbar-actions">
              <Button disabled={busy} onClick={() => void decide(false)}>
                Deny
              </Button>
              <Button variant="primary" disabled={busy} onClick={() => void decide(true)}>
                {busy ? 'Submitting…' : 'Authorize CLI'}
              </Button>
            </div>
          </section>
        )
      )}
    </>
  )
}
