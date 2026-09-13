import { useState } from 'react'
import { Link } from '@tanstack/react-router'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { client, unwrap } from '../lib/client'
import { message, timestamp } from '../lib/api'
import { providerNames, scopeSummary, type SecretProvider } from '../lib/secret-providers'
import { useScope } from '../lib/scope'
import { Button } from './ui/button'
import { Input } from './ui/input'
import { Dialog } from './ui/dialog'
import { Icon } from './icons'
import { SecretProviderIcon } from './secret-provider-icon'
import { Empty, ErrorState, HeadingHelp, Loading, Note, PageHeader } from './shared'

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
  const [confirmation, setConfirmation] = useState('')
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
          description="Connect a provider to use its secrets in application TOML."
        />
      ) : (
        <div className="grid gap-0">
          {items.map((provider) => (
            <div
              className="flex flex-wrap items-start justify-between gap-4 border-b border-[var(--hairline)] py-4 last:border-b-0"
              key={provider.name}
            >
              <div className="flex min-w-0 flex-1 gap-3">
                <div className="mt-1 shrink-0">
                  <SecretProviderIcon kind={provider.kind} />
                </div>
                <div className="grid min-w-0 gap-1">
                  <strong className="break-all">{provider.name}</strong>
                  <span className="text-sm">{providerNames[provider.kind]}</span>
                  <span className="muted-text break-all text-sm">{provider.endpoint}</span>
                  <p className="m-0 break-words text-sm">
                    {provider.scopes.map(scopeSummary).join(' · ')}
                  </p>
                  <small className="muted-text">
                    Revision {provider.revision} · updated {timestamp(provider.updated_at)}
                  </small>
                </div>
              </div>
              <div className="flex shrink-0 items-center gap-2">
                <Button asChild size="sm">
                  <Link
                    to="/settings/secret-providers/$providerName/edit"
                    params={{ providerName: provider.name }}
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
                    setConfirmation('')
                    setError('')
                  }}
                >
                  <Icon name="trash" size={15} />
                </Button>
              </div>
            </div>
          ))}
        </div>
      )}
      <Dialog
        open={Boolean(removing)}
        onOpenChange={(open) => {
          if (!busy && !open) setRemoving(null)
        }}
        title={`Delete ${removing?.name || 'provider'}?`}
        description="Remove its saved credentials and access grants. Providers still referenced by applications cannot be deleted."
      >
        <form
          onSubmit={async (event) => {
            event.preventDefault()
            if (!removing || confirmation !== removing.name || busy) return
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
          <div className="dialog-body field-stack">
            <label className="min-w-0 break-all">
              Type {removing?.name} to confirm
              <Input
                required
                value={confirmation}
                onChange={(event) => setConfirmation(event.target.value)}
                autoComplete="off"
              />
            </label>
            {error && (
              <div className="inline-error" role="alert">
                {error}
              </div>
            )}
          </div>
          <div className="dialog-footer">
            <Button type="button" disabled={busy} onClick={() => setRemoving(null)}>
              Keep provider
            </Button>
            <Button
              type="submit"
              variant="danger"
              disabled={busy || confirmation !== removing?.name}
            >
              <Icon name="trash" size={15} />
              {busy ? 'Deleting…' : 'Delete provider'}
            </Button>
          </div>
        </form>
      </Dialog>
    </>
  )
}
