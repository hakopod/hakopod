import { useState } from 'react'
import { createFileRoute } from '@tanstack/react-router'
import { useQuery } from '@tanstack/react-query'
import { client, unwrap } from '../lib/client'
import { editionFetch } from '../lib/client-edition'
import { APIError, message } from '../lib/api'
import { Button } from '../components/ui/button'
import { ErrorState, Loading, RequestError } from '../components/shared'
import { Icon } from '../components/icons'

const fields = [
  'client_id',
  'redirect_uri',
  'response_type',
  'code_challenge',
  'code_challenge_method',
  'resource',
  'state',
] as const

// OAuth authorization endpoint for MCP clients. The request becomes a device
// code that the signed-in user approves; the code is the authorization code.
export const Route = createFileRoute('/login/authorize')({
  validateSearch: (search: Record<string, unknown>) =>
    Object.fromEntries(
      fields.map((name) => [
        name,
        typeof search[name] === 'string' ? (search[name] as string).slice(0, 2048) : '',
      ]),
    ) as Record<(typeof fields)[number], string>,
  component: OAuthConsent,
})

type Grant = {
  device_code: string
  user_code: string
  client_name: string
  redirect_host: string
  project: string
  environment: string
  permissions: string[]
  issuer: string
}

const labels: Record<string, string> = {
  'deployments:read': 'See applications and deployments',
  'logs:read': 'Read logs',
  'deployments:write': 'Deploy and change applications',
}

function OAuthConsent() {
  const { state, ...query } = Route.useSearch()
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const grant = useQuery({
    queryKey: ['oauth-authorize', query],
    queryFn: async ({ signal }) => {
      const response = await editionFetch('/api/oauth/authorize', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(query),
        credentials: 'same-origin',
        cache: 'no-store',
        signal,
      })
      const body = await response.json().catch(() => ({}))
      if (!response.ok)
        throw new APIError(
          body?.error?.message || `Request failed (${response.status}).`,
          response.status,
          body?.error?.code,
        )
      const grant = body as Grant
      const details = await unwrap(
        client.GET('/auth/device', { signal, params: { query: { user_code: grant.user_code } } }),
      )
      const scope = details.scopes?.find(
        (scope) => scope.project === grant.project && scope.environment === grant.environment,
      )
      return { ...grant, scope }
    },
    enabled: Boolean(query.client_id && query.redirect_uri),
    retry: false,
    gcTime: 0,
    staleTime: Infinity,
    refetchOnWindowFocus: false,
  })
  async function decide(approve: boolean) {
    const data = grant.data
    if (busy || !data) return
    setBusy(true)
    setError('')
    try {
      await unwrap(
        client.POST('/auth/device/approve', {
          body: {
            user_code: data.user_code,
            approve,
            ...(data.scope
              ? {
                  scope_id: data.scope.id,
                  project: data.scope.project,
                  environment: data.scope.environment,
                }
              : {}),
          },
        }),
      )
      // The server checked this redirect URI exactly against the registration.
      const target = new URL(query.redirect_uri)
      if (approve) target.searchParams.set('code', data.device_code)
      else target.searchParams.set('error', 'access_denied')
      if (state) target.searchParams.set('state', state)
      target.searchParams.set('iss', data.issuer)
      window.location.assign(target.toString())
    } catch (err) {
      setError(message(err))
      setBusy(false)
    }
  }
  const data = grant.data
  return (
    <div className="device-consent">
      {!query.client_id || !query.redirect_uri ? (
        <section className="device-card device-message" aria-labelledby="device-title">
          <span className="device-mark" aria-hidden="true">
            <Icon name="lock" size={18} />
          </span>
          <h1 id="device-title">Incomplete sign-in request</h1>
          <p>Start the connection again from your MCP client.</p>
        </section>
      ) : grant.isPending ? (
        <section className="device-card">
          <Loading />
        </section>
      ) : grant.error ? (
        <section className="device-card">
          <ErrorState error={grant.error} />
        </section>
      ) : (
        data && (
          <section className="device-card" aria-labelledby="device-title">
            <header className="device-head">
              <span className="device-mark" aria-hidden="true">
                <Icon name="lock" size={18} />
              </span>
              <h1 id="device-title">Connect {data.client_name}</h1>
              <p>
                {data.client_name} wants access to project <strong>{data.project}</strong>,
                environment <strong>{data.environment}</strong>. After you decide, you return to{' '}
                <strong>{data.redirect_host}</strong>.
              </p>
            </header>
            <div className="device-section">
              <h2>It will be able to</h2>
              <ul className="device-permissions">
                {data.permissions.map((permission) => (
                  <li key={permission}>
                    <Icon name="check" size={14} />
                    <span>{labels[permission] || permission}</span>
                    <code>{permission}</code>
                  </li>
                ))}
              </ul>
              {!data.scope && (
                <p className="device-warning">
                  You cannot grant this access to {data.project} / {data.environment}. Ask a project
                  administrator, or deny the request.
                </p>
              )}
            </div>
            {error && <RequestError error={error} />}
            <footer className="device-actions">
              <Button disabled={busy} onClick={() => void decide(false)}>
                Deny
              </Button>
              <Button
                variant="primary"
                disabled={busy || !data.scope}
                onClick={() => void decide(true)}
              >
                {busy ? 'Submitting…' : 'Approve'}
              </Button>
            </footer>
          </section>
        )
      )}
    </div>
  )
}
