import { useState } from 'react'
import { createFileRoute, Link, Navigate, Outlet, useLocation } from '@tanstack/react-router'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { client, unwrap } from '../lib/client'
import { message, timestamp } from '../lib/api'
import { useScope } from '../lib/scope'
import {
  dnsProviderKinds,
  dnsProviderScope,
  storedDNSProviders,
  type DNSProvider,
} from '../components/dns-provider-credential-fields'
import { Button } from '../components/ui/button'
import { Dialog } from '../components/ui/dialog'
import { Icon } from '../components/icons'
import { ErrorState, Loading, Note, RequestError } from '../components/shared'
import { focusedDialog, plural, ReviewFacts } from '../components/operation-review'
import {
  SettingsEmpty,
  SettingsRow,
  SettingsRows,
  SettingsSection,
  SettingsState,
} from '../components/settings-section'

export const Route = createFileRoute('/settings/dns-providers')({
  validateSearch: (search: Record<string, unknown>): { saved?: string } => ({
    saved:
      typeof search.saved === 'string' &&
      search.saved.length > 0 &&
      search.saved.length <= 80 &&
      !/[\x00-\x1f]/.test(search.saved)
        ? search.saved
        : undefined,
  }),
  component: DNSProvidersRoute,
})

function DNSProvidersRoute() {
  const path = useLocation().pathname
  const { saved } = Route.useSearch()
  if (path !== '/settings/dns-providers') return <Outlet />
  // The provider list lives in the settings hub; task pages return there.
  return <Navigate to="/settings" search={{ tab: 'dns-providers', saved }} replace />
}

export function DNSProviders({ saved }: { saved?: string }) {
  const scope = useScope()
  const cache = useQueryClient()
  const providers = useQuery({
    queryKey: ['dns-providers'],
    queryFn: ({ signal }) => unwrap(client.GET('/dns-providers', { signal })),
    enabled: scope.identity.admin,
    gcTime: 0,
  })
  const [removing, setRemoving] = useState<DNSProvider | null>(null)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  if (!scope.identity.admin)
    return (
      <SettingsSection title="DNS providers" state="Administrator access required">
        <SettingsEmpty title="Not available to your role">
          DNS provider credentials are configured by installation administrators.
        </SettingsEmpty>
      </SettingsSection>
    )
  const items = storedDNSProviders(providers.data?.items || [])
  const add = (
    <Button asChild variant="primary">
      <Link to="/settings/dns-providers/new">
        <Icon name="plus" size={15} />
        Add credential
      </Link>
    </Button>
  )
  const unavailable = items.filter((provider) => !provider.enabled).length
  return (
    <SettingsSection
      title="DNS providers"
      help="Store a DNS provider token so the ownership and routing records for a custom domain can be created for you. Entering the records by hand stays available."
      state={
        providers.data
          ? items.length
            ? `${plural(items.length, 'credential')}${unavailable ? ` · ${unavailable} not available for creating records` : ''}`
            : 'Domain records are entered by hand'
          : undefined
      }
      actions={items.length > 0 && add}
    >
      {saved && (
        <div role="status">
          <Note>
            <span className="break-all">{saved}</span> saved. Creating records is not verifying
            them; a domain stays awaiting DNS until its records have propagated and it is verified.
          </Note>
        </div>
      )}
      {providers.isPending ? (
        <Loading />
      ) : providers.error ? (
        <ErrorState error={providers.error} retry={() => void providers.refetch()} />
      ) : items.length === 0 ? (
        <SettingsEmpty title="No DNS provider credentials" action={add}>
          Add a credential to create custom domain records without leaving Hakopod.
        </SettingsEmpty>
      ) : (
        <SettingsRows label="DNS provider credentials">
          {items.map((provider) => (
            <SettingsRow
              key={`${provider.project}/${provider.environment}/${provider.name}`}
              mark={<Icon name="globe" size={16} />}
              name={provider.name}
              detail={`${dnsProviderKinds[provider.kind as keyof typeof dnsProviderKinds] || provider.kind} · ${dnsProviderScope(provider)}`}
              status={
                <SettingsState tone={provider.enabled ? 'ok' : 'muted'}>
                  {provider.enabled ? 'Creates records' : 'Not available'}
                </SettingsState>
              }
              meta={
                <>
                  <span>Zones {provider.zone_filter.join(', ')}</span>
                  <span>
                    Revision {provider.revision}
                    {provider.updated_at ? ` · updated ${timestamp(provider.updated_at)}` : ''}
                  </span>
                </>
              }
              actions={
                <>
                  <Button asChild size="sm">
                    <Link
                      to="/settings/dns-providers/$providerName/edit"
                      params={{ providerName: provider.name }}
                      search={{
                        project: provider.project || undefined,
                        environment: provider.environment || undefined,
                      }}
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
        title={`Delete ${removing?.name || 'credential'}?`}
        description="The stored token is erased and cannot be shown again."
      >
        <form
          onSubmit={async (event) => {
            event.preventDefault()
            if (!removing || busy) return
            setBusy(true)
            setError('')
            try {
              await unwrap(
                client.DELETE('/dns-providers/{name}', {
                  params: { path: { name: removing.name } },
                  body: {
                    project: removing.project || '',
                    environment: removing.environment || '',
                    expected_revision: removing.revision,
                  },
                }),
              )
              setRemoving(null)
              void cache.invalidateQueries({ queryKey: ['dns-providers'] })
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
                  ['Credential', `${removing.name} · revision ${removing.revision}`],
                  ['Available to', dnsProviderScope(removing)],
                  ['Removed', 'The stored token. New domains need their records entered by hand.'],
                  ['Kept', 'Records already created at the provider, and existing domains.'],
                ]}
              />
            )}
            {error && <RequestError error={error} />}
          </div>
          <div className="dialog-footer">
            <Button type="button" disabled={busy} onClick={() => setRemoving(null)}>
              Cancel
            </Button>
            <Button type="submit" variant="danger" aria-busy={busy || undefined} disabled={busy}>
              <Icon name="trash" size={15} />
              {busy ? 'Deleting…' : `Delete ${removing?.name || 'credential'}`}
            </Button>
          </div>
        </form>
      </Dialog>
    </SettingsSection>
  )
}
