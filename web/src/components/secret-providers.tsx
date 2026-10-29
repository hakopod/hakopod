import { useState } from 'react'
import { Link } from '@tanstack/react-router'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { client, unwrap } from '../lib/client'
import { message, timestamp } from '../lib/api'
import { providerNames, scopeSummary, type SecretProvider } from '../lib/secret-providers'
import { useScope } from '../lib/scope'
import { Button } from './ui/button'
import { Dialog } from './ui/dialog'
import { Icon } from './icons'
import { SecretProviderIcon } from './secret-provider-icon'
import { focusedDialog, ReviewFacts, ReviewNotice } from './operation-review'
import { Empty, ErrorState, HeadingHelp, Loading, Note, PageHeader, RequestError } from './shared'

export function SecretProviders({
  saved,
  standalone = false,
}: {
  saved?: string
  standalone?: boolean
}) {
  const scope = useScope()
  const cache = useQueryClient()
  const providers = useQuery({
    queryKey: ['secret-providers'],
    queryFn: ({ signal }) => unwrap(client.GET('/secret-providers', { signal })),
    enabled: scope.identity.admin,
    gcTime: 0,
  })
  const [removing, setRemoving] = useState<SecretProvider | null>(null)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  if (!scope.identity.admin)
    return (
      <Empty
        icon="lock"
        title="Administrator access required"
        description="Secret providers are configured by installation administrators."
      />
    )
  const items = (providers.data?.items || []).filter(
    (item): item is SecretProvider => 'endpoint' in item,
  )
  const add = (
    <Button asChild variant="primary">
      <Link to="/settings/secret-providers/new">
        <Icon name="plus" size={15} />
        Add provider
      </Link>
    </Button>
  )
  return (
    <>
      {standalone ? (
        <PageHeader
          title="Secret providers"
          description="Connect Vault/OpenBao or Infisical and grant access to selected projects."
          action={add}
        />
      ) : (
        <div className="section-toolbar">
          <div className="hako-section-heading-title">
            <h2>Secret providers</h2>
            <HeadingHelp title="Secret providers">
              Connect Vault/OpenBao or Infisical and grant access to selected projects. Native
              application secrets remain available.
            </HeadingHelp>
          </div>
          {add}
        </div>
      )}
      {saved && (
        <div role="status" className="mb-3">
          <Note>
            <span className="break-all">{saved}</span> saved. External values are read on the next
            deployment; restart affected services after rotating a secret.
          </Note>
        </div>
      )}
      {providers.isPending ? (
        <Loading />
      ) : providers.error ? (
        <ErrorState error={providers.error} retry={() => void providers.refetch()} />
      ) : items.length === 0 ? (
        <Empty
          icon="lock"
          title="No external secret providers"
          description="Applications can only use native secrets until a provider is connected."
          action={add}
        />
      ) : (
        <ul className="settings-rows" aria-label="Secret providers">
          {items.map((provider) => (
            <li className="settings-row" key={provider.name}>
              <div className="settings-row-identity">
                <Icon name="lock" size={16} />
                <div>
                  <strong>{provider.name}</strong>
                  <small>{provider.endpoint}</small>
                </div>
              </div>
              <span className="settings-row-status secret-provider-kind">
                <SecretProviderIcon kind={provider.kind} />
                {providerNames[provider.kind]}
              </span>
              <div className="settings-row-meta">
                <span>{provider.scopes.map(scopeSummary).join(' · ') || 'No project access'}</span>
                <span>
                  Revision {provider.revision} · updated {timestamp(provider.updated_at)}
                </span>
              </div>
              <div className="settings-row-actions">
                <Button asChild size="sm">
                  <Link
                    to="/settings/secret-providers/$providerName/edit"
                    params={{ providerName: provider.name }}
                    aria-label={`Edit ${provider.name}`}
                  >
                    Edit
                  </Link>
                </Button>
                <Button
                  size="sm"
                  variant="ghost"
                  aria-label={`Delete ${provider.name}`}
                  onClick={() => {
                    setRemoving(provider)
                    setError('')
                  }}
                >
                  <Icon name="trash" size={15} />
                </Button>
              </div>
            </li>
          ))}
        </ul>
      )}
      <Dialog
        open={Boolean(removing)}
        className={focusedDialog}
        onOpenChange={(open) => {
          if (!busy && !open) setRemoving(null)
        }}
        title={`Delete ${removing?.name || 'provider'}?`}
        description="Its saved credentials are erased and cannot be shown again."
      >
        <form
          onSubmit={async (event) => {
            event.preventDefault()
            if (!removing || busy) return
            setBusy(true)
            setError('')
            try {
              await unwrap(
                client.DELETE('/secret-providers/{name}', {
                  params: { path: { name: removing.name } },
                  body: { expected_revision: removing.revision },
                }),
              )
              setRemoving(null)
              void cache.invalidateQueries({ queryKey: ['secret-providers'] })
              void cache.invalidateQueries({ queryKey: ['secret-provider', removing.name] })
            } catch (cause) {
              setError(message(cause))
            } finally {
              setBusy(false)
            }
          }}
        >
          <div className="dialog-body">
            {removing && (
              <ReviewFacts
                facts={[
                  [
                    'Provider',
                    <>
                      {removing.name} · {providerNames[removing.kind]} · revision{' '}
                      {removing.revision}
                    </>,
                  ],
                  ['Endpoint', removing.endpoint],
                  ['Removed', 'Saved credentials and project access grants.'],
                  [
                    'Kept',
                    'Secrets stored in the provider itself, and native application secrets.',
                  ],
                ]}
              />
            )}
            <ReviewNotice title="Providers in use cannot be deleted">
              If an application still references this provider, the server refuses the deletion and
              nothing changes.
            </ReviewNotice>
            {error && <RequestError error={error} />}
          </div>
          <div className="dialog-footer">
            <Button type="button" disabled={busy} onClick={() => setRemoving(null)}>
              Cancel
            </Button>
            <Button type="submit" variant="danger" aria-busy={busy || undefined} disabled={busy}>
              <Icon name="trash" size={15} />
              {busy ? 'Deleting…' : `Delete ${removing?.name || 'provider'}`}
            </Button>
          </div>
        </form>
      </Dialog>
    </>
  )
}
