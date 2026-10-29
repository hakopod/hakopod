import { useState } from 'react'
import { createFileRoute } from '@tanstack/react-router'
import { useQuery } from '@tanstack/react-query'
import { client, unwrap } from '../lib/client'
import { message, timestamp } from '../lib/api'
import { SelectField } from '../components/ui/select'
import { dashboardEdition } from '../lib/dashboard-edition'
import { Button } from '../components/ui/button'
import { ErrorState, Loading, RequestError } from '../components/shared'
import { Icon } from '../components/icons'

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
  const installation = request.data?.permissions.includes('agent:admin') || false
  const credentials = request.data?.permissions.includes('agent:credentials') || false
  const hostTerminal = request.data?.permissions.includes('nodes:terminal') || false
  const hostOnly = hostTerminal && !installation
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
  const permissions = request.data?.permissions || []
  return (
    <div className="device-consent">
      {done !== null ? (
        <DeviceMessage
          icon={done ? 'check' : 'lock'}
          title={done ? 'Terminal connected' : 'Request denied'}
          description="You can return to your terminal and close this page."
        />
      ) : !user_code ? (
        <DeviceMessage
          icon="terminal"
          title="Missing device code"
          description="Run hakopod login in your terminal and open the link it prints."
        />
      ) : request.isPending ? (
        <section className="device-card">
          <Loading />
        </section>
      ) : request.error ? (
        <section className="device-card">
          <ErrorState error={request.error} />
        </section>
      ) : (
        request.data && (
          <section className="device-card" aria-labelledby="device-title">
            <header className="device-head">
              <span className="device-mark" aria-hidden="true">
                <Icon name="terminal" size={18} />
              </span>
              <h1 id="device-title">Connect your terminal</h1>
              <p>
                Your Hakopod CLI is asking for access. Approve only if this code matches the one in
                your terminal.
              </p>
            </header>
            <div className="device-code">
              <code aria-label="Device code">{request.data.user_code}</code>
              <span>Expires {timestamp(request.data.expires_at)}</span>
            </div>
            <div className="device-section">
              <h2>It will be able to</h2>
              <ul className="device-permissions">
                {permissions.map((permission) => {
                  const known = permissionLabels[permission]
                  return (
                    <li key={permission} data-risk={known?.risk || undefined}>
                      <Icon name={known?.risk ? 'alert' : 'check'} size={14} />
                      <span>{known?.label || permission}</span>
                      {known && <code>{permission}</code>}
                    </li>
                  )
                })}
              </ul>
              {installation && (
                <p className="device-warning">
                  This grants installation administration, including projects, users and settings.
                  Pod commands and database queries require separate project connections.
                </p>
              )}
              {credentials && (
                <p className="device-warning">
                  Enabled agent tools can retrieve credentials. Retrieved secrets can enter the
                  connected agent&apos;s context.
                </p>
              )}
              {hostTerminal && (
                <p className="device-warning">
                  Host terminal commands run on authorized nodes. Hakopod checks your current host
                  access for each request.
                </p>
              )}
            </div>
            <div className="device-section">
              <SelectField
                label={
                  installation
                    ? 'Installation'
                    : hostOnly
                      ? 'Host access'
                      : 'Deployment destination'
                }
                value={selection}
                onValueChange={setSelection}
                required
                disabled={busy}
                options={[
                  {
                    value: '',
                    label: installation
                      ? 'Choose an installation'
                      : hostOnly
                        ? 'Choose host access'
                        : 'Choose a destination',
                  },
                  ...scopes.map((scope) => ({
                    value: JSON.stringify([scope.id, scope.project, scope.environment]),
                    label: scope.label,
                  })),
                ]}
              />
              {scopes.length === 0 && (
                <p className="field-help">
                  {installation
                    ? 'No installation is available. Refresh the list or contact your administrator.'
                    : hostOnly
                      ? 'No host access is available. Ask your installation owner for a host grant, then refresh this list.'
                      : `No deployment destination is ready. Set up a project${dashboardEdition.cloud ? ' and its compute' : ''}, then refresh this list.`}
                </p>
              )}
              <div className="device-links">
                <Button
                  variant="ghost"
                  size="sm"
                  disabled={request.isFetching || busy}
                  onClick={() => void request.refetch()}
                >
                  <Icon name="refresh" size={14} />
                  Refresh list
                </Button>
                <Button asChild variant="ghost" size="sm">
                  <a href="/" target="_blank" rel="noopener noreferrer">
                    <Icon name="external" size={14} />
                    Open dashboard setup
                  </a>
                </Button>
              </div>
            </div>
            {error && <RequestError error={error} />}
            <footer className="device-actions">
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
            </footer>
          </section>
        )
      )}
    </div>
  )
}

// Plain wording for the permissions a CLI can request; powerful ones are marked.
const permissionLabels: Record<string, { label: string; risk?: boolean }> = {
  'deployments:read': { label: 'See applications and deployments' },
  'deployments:write': { label: 'Deploy and change applications' },
  'logs:read': { label: 'Read logs' },
  'jobs:read': { label: 'See jobs' },
  'sessions:read': { label: 'See sign-in sessions' },
  'networks:write': { label: 'Change private networks' },
  'pods:exec': { label: 'Run commands in service containers', risk: true },
  'databases:query': { label: 'Query databases', risk: true },
  'agent:credentials': { label: 'Retrieve credentials', risk: true },
  'agent:admin': { label: 'Administer the installation', risk: true },
  'nodes:terminal': { label: 'Open a terminal on authorized hosts', risk: true },
}

function DeviceMessage({
  icon,
  title,
  description,
}: {
  icon: string
  title: string
  description: string
}) {
  return (
    <section className="device-card device-message" aria-labelledby="device-title">
      <span className="device-mark" aria-hidden="true">
        <Icon name={icon} size={18} />
      </span>
      <h1 id="device-title">{title}</h1>
      <p>{description}</p>
    </section>
  )
}
