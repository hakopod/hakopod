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
import { focusedDialog, plural, ReviewFacts, ReviewNotice } from './operation-review'
import { ErrorState, Loading, Note, RequestError } from './shared'
import { SettingsEmpty, SettingsRow, SettingsRows, SettingsSection } from './settings-section'

export function SecretProviders({ saved }: { saved?: string }) {
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
      <SettingsSection title="Secret providers" state="Administrator access required">
        <SettingsEmpty title="Not available to your role">
          Secret providers are configured by installation administrators.
        </SettingsEmpty>
      </SettingsSection>
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
  const unused = items.filter((provider) => !provider.scopes.length).length
  return (
    <SettingsSection
      title="Secret providers"
      help="Connect Vault/OpenBao or Infisical and grant access to selected projects. Native application secrets remain available."
      state={
        providers.data
          ? items.length
            ? `${plural(items.length, 'provider')}${unused ? ` · ${unused} without project access` : ''}`
            : 'Native application secrets only'
          : undefined
      }
      actions={items.length > 0 && add}
    >
      {saved && (
        <div role="status">
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
        <SettingsEmpty title="No external secret providers" action={add}>
          Applications can only use native secrets until a provider is connected.
        </SettingsEmpty>
      ) : (
        <SettingsRows label="Secret providers">
          {items.map((provider) => (
            <SettingsRow
              key={provider.name}
              mark={<Icon name="lock" size={16} />}
              name={provider.name}
              detail={provider.endpoint}
              status={
                <span className="secret-provider-kind">
                  <SecretProviderIcon kind={provider.kind} />
                  {providerNames[provider.kind]}
                </span>
              }
              meta={
                <>
                  <span>
                    {provider.scopes.map(scopeSummary).join(' · ') || 'No project access'}
                  </span>
                  <span>
                    Revision {provider.revision} · updated {timestamp(provider.updated_at)}
                  </span>
                </>
              }
              actions={
                <>
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
                </>
              }
            />
          ))}
        </SettingsRows>
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
    </SettingsSection>
  )
}
