import { useState } from 'react'
import { createFileRoute, Link, Outlet, useLocation } from '@tanstack/react-router'
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
import { Input } from '../components/ui/input'
import { Dialog } from '../components/ui/dialog'
import { Icon } from '../components/icons'
import {
  Empty,
  ErrorState,
  HeadingHelp,
  Loading,
  Note,
  PageHeader,
  RequestError,
} from '../components/shared'

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
  return <DNSProviders standalone saved={saved} />
}

export function DNSProviders({
  saved,
  standalone = false,
}: {
  saved?: string
  standalone?: boolean
}) {
  const scope = useScope()
  const cache = useQueryClient()
  const providers = useQuery({
    queryKey: ['dns-providers'],
    queryFn: ({ signal }) => unwrap(client.GET('/dns-providers', { signal })),
    enabled: scope.identity.admin,
    gcTime: 0,
  })
  const [removing, setRemoving] = useState<DNSProvider | null>(null)
  const [confirmation, setConfirmation] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  if (!scope.identity.admin)
    return (
      <Empty
        icon="lock"
        title="Administrator access required"
        description="DNS provider credentials are configured by installation administrators."
      />
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
  return (
    <>
      {standalone ? (
        <PageHeader
          title="DNS providers"
          description="Store a DNS provider token so custom domain records can be created for you."
          action={add}
        />
      ) : (
        <div className="section-toolbar">
          <div className="hako-section-heading-title">
            <h2>DNS providers</h2>
            <HeadingHelp title="DNS providers">
              Store a DNS provider token so the ownership and routing records for a custom domain
              can be created for you. Entering the records by hand stays available.
            </HeadingHelp>
          </div>
          {add}
        </div>
      )}
      {saved && (
        <div role="status" className="mb-3">
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
        <Empty
          icon="globe"
          title="No DNS provider credentials"
          description="Add a credential to create custom domain records without leaving hakopod."
        />
      ) : (
        <div className="grid gap-0">
          {items.map((provider) => (
            <div
              className="flex flex-wrap items-start justify-between gap-4 border-b border-[var(--hairline)] py-4 last:border-b-0"
              key={provider.name}
            >
              <div className="grid min-w-0 flex-1 gap-1">
                <strong className="break-all">{provider.name}</strong>
                <span className="text-sm">
                  {dnsProviderKinds[provider.kind as keyof typeof dnsProviderKinds] ||
                    provider.kind}
                  {provider.enabled ? '' : ' · not available for creating records'}
                </span>
                <p className="m-0 break-words text-sm">{dnsProviderScope(provider)}</p>
                <p className="muted-text m-0 break-all text-sm">
                  Zones {provider.zone_filter.join(', ')}
                </p>
                <small className="muted-text">
                  Revision {provider.revision}
                  {provider.updated_at ? ` · updated ${timestamp(provider.updated_at)}` : ''}
                </small>
              </div>
              <div className="flex shrink-0 items-center gap-2">
                <Button asChild size="sm">
                  <Link
                    to="/settings/dns-providers/$providerName/edit"
                    params={{ providerName: provider.name }}
                    search={{
                      project: provider.project || undefined,
                      environment: provider.environment || undefined,
                    }}
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
        title={`Delete ${removing?.name || 'credential'}?`}
        description="Remove the stored token. Records already created at the provider are left in place."
      >
        <form
          onSubmit={async (event) => {
            event.preventDefault()
            if (!removing || confirmation !== removing.name || busy) return
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
            {error && <RequestError error={error} />}
          </div>
          <div className="dialog-footer">
            <Button type="button" disabled={busy} onClick={() => setRemoving(null)}>
              Keep credential
            </Button>
            <Button
              type="submit"
              variant="danger"
              disabled={busy || confirmation !== removing?.name}
            >
              <Icon name="trash" size={15} />
              {busy ? 'Deleting…' : 'Delete credential'}
            </Button>
          </div>
        </form>
      </Dialog>
    </>
  )
}
