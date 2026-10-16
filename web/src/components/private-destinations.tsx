import { useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { client, unwrap } from '../lib/client'
import { message, timestamp } from '../lib/api'
import type { components } from '../lib/api.generated'
import { Button } from './ui/button'
import { Input } from './ui/input'
import { Dialog } from './ui/dialog'
import { Icon } from './icons'
import { Empty, ErrorState, HeadingHelp, Loading, RequestError } from './shared'

type Binding = components['schemas']['PrivateEgressBinding']
type Draft = Record<
  'name' | 'project' | 'environment' | 'application' | 'services' | 'cidrs' | 'ports',
  string
> & {
  revision: number
}

const blank: Draft = {
  name: '',
  project: '',
  environment: '',
  application: '',
  services: '',
  cidrs: '',
  ports: '',
  revision: 0,
}
const list = (value: string) => value.split(/[\s,]+/).filter(Boolean)
const draftOf = (b: Binding, revision: number): Draft => ({
  ...b,
  services: b.services.join(', '),
  cidrs: b.cidrs.join(', '),
  ports: b.ports.join(', '),
  revision,
})

export function PrivateDestinations() {
  const cache = useQueryClient()
  const bindings = useQuery({
    queryKey: ['private-egress-bindings'],
    queryFn: ({ signal }) => unwrap(client.GET('/admin/private-egress-bindings', { signal })),
    gcTime: 0,
  })
  const [draft, setDraft] = useState<Draft | null>(null)
  const [removing, setRemoving] = useState<{ name: string; revision: number } | null>(null)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const refresh = () => void cache.invalidateQueries({ queryKey: ['private-egress-bindings'] })
  const run = async (action: () => Promise<unknown>, done: () => void) => {
    setBusy(true)
    setError('')
    try {
      await action()
      done()
    } catch (cause) {
      setError(message(cause))
    } finally {
      setBusy(false)
    }
  }
  const field = (key: keyof Omit<Draft, 'revision'>, label: string, hint?: string) => (
    <label key={key}>
      {label}
      <Input
        required
        value={draft?.[key] || ''}
        disabled={key === 'name' && Boolean(draft?.revision)}
        placeholder={hint}
        onChange={(event) => draft && setDraft({ ...draft, [key]: event.target.value })}
        autoComplete="off"
      />
    </label>
  )
  return (
    <>
      <div className="section-toolbar">
        <div className="hako-section-heading-title">
          <h2>Private destinations</h2>
          <HeadingHelp title="Private destinations">
            Private egress is closed by default. Approve exact database subnets and TCP ports for
            named services. Changes apply on the next plan and reconcile without a restart.
          </HeadingHelp>
        </div>
        <div className="flex gap-2">
          <Button
            variant="primary"
            onClick={() => {
              setDraft(blank)
              setError('')
            }}
          >
            <Icon name="plus" size={15} />
            Add destination
          </Button>
        </div>
      </div>
      {bindings.isPending ? (
        <Loading />
      ) : bindings.error ? (
        <ErrorState error={bindings.error} retry={() => void bindings.refetch()} />
      ) : bindings.data.items.length + bindings.data.managed_by_file.length === 0 ? (
        <Empty
          icon="lock"
          title="No private destinations"
          description="Applications cannot reach private networks until a destination is approved."
        />
      ) : (
        <div className="grid gap-0">
          {[
            ...bindings.data.items.map((r) => ({ binding: r.config, record: r })),
            ...bindings.data.managed_by_file.map((binding) => ({ binding, record: null })),
          ].map(({ binding, record }) => (
            <div
              className="flex flex-wrap items-start justify-between gap-4 border-b border-[var(--hairline)] py-4 last:border-b-0"
              key={binding.name}
            >
              <div className="grid min-w-0 flex-1 gap-1">
                <strong className="break-all">{binding.name}</strong>
                <span className="text-sm">
                  {binding.project} / {binding.environment} / {binding.application} · services{' '}
                  {binding.services.join(', ')}
                </span>
                <span className="muted-text break-all text-sm">
                  {binding.cidrs.join(', ')} · TCP {binding.ports.join(', ')}
                </span>
                <small className="muted-text">
                  {record
                    ? `Revision ${record.revision} · updated ${timestamp(record.updated_at)}`
                    : 'Managed by file (read-only)'}
                </small>
              </div>
              {record && (
                <div className="flex shrink-0 items-center gap-2">
                  <Button
                    size="sm"
                    onClick={() => {
                      setDraft(draftOf(binding, record.revision))
                      setError('')
                    }}
                  >
                    Edit
                  </Button>
                  <Button
                    size="sm"
                    variant="ghost"
                    aria-label={`Delete ${binding.name}`}
                    onClick={() => {
                      setRemoving({ name: binding.name, revision: record.revision })
                      setError('')
                    }}
                  >
                    <Icon name="trash" size={15} />
                  </Button>
                </div>
              )}
            </div>
          ))}
        </div>
      )}
      <Dialog
        open={Boolean(draft)}
        onOpenChange={(open) => !busy && !open && setDraft(null)}
        title={draft?.revision ? `Edit ${draft.name}` : 'Add private destination'}
        description="Use the database's subnet CIDRs, not the whole VPC. Never include node addresses."
      >
        <form
          onSubmit={(event) => {
            event.preventDefault()
            if (!draft || busy) return
            void run(
              () =>
                unwrap(
                  client.PUT('/admin/private-egress-bindings/{name}', {
                    params: { path: { name: draft.name } },
                    body: {
                      name: draft.name,
                      project: draft.project,
                      environment: draft.environment,
                      application: draft.application,
                      services: list(draft.services),
                      cidrs: list(draft.cidrs),
                      ports: list(draft.ports).map(Number),
                      expected_revision: draft.revision,
                    },
                  }),
                ),
              () => {
                setDraft(null)
                refresh()
              },
            )
          }}
        >
          <div className="dialog-body field-stack">
            {field('name', 'Name', 'orders-db')}
            {field('project', 'Project')}
            {field('environment', 'Environment')}
            {field('application', 'Application')}
            {field('services', 'Services', 'api, worker')}
            {field('cidrs', 'CIDRs', '10.20.30.0/24')}
            {field('ports', 'TCP ports', '5432')}
            {error && <RequestError error={error} />}
          </div>
          <div className="dialog-footer">
            <Button type="button" disabled={busy} onClick={() => setDraft(null)}>
              Cancel
            </Button>
            <Button type="submit" variant="primary" disabled={busy}>
              {busy ? 'Saving…' : 'Save destination'}
            </Button>
          </div>
        </form>
      </Dialog>
      <Dialog
        open={Boolean(removing)}
        onOpenChange={(open) => !busy && !open && setRemoving(null)}
        title={`Revoke ${removing?.name || 'destination'}?`}
        description="New plans that reference it fail at once; running services lose the rule on the next maintenance pass."
      >
        <div className="dialog-body">{error && <RequestError error={error} />}</div>
        <div className="dialog-footer">
          <Button type="button" disabled={busy} onClick={() => setRemoving(null)}>
            Keep
          </Button>
          <Button
            variant="danger"
            disabled={busy}
            onClick={() =>
              removing &&
              void run(
                () =>
                  unwrap(
                    client.DELETE('/admin/private-egress-bindings/{name}', {
                      params: { path: { name: removing.name } },
                      body: { expected_revision: removing.revision },
                    }),
                  ),
                () => {
                  setRemoving(null)
                  refresh()
                },
              )
            }
          >
            <Icon name="trash" size={15} />
            {busy ? 'Revoking…' : 'Revoke'}
          </Button>
        </div>
      </Dialog>
    </>
  )
}
