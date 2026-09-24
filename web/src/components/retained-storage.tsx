import { useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { client, unwrap } from '../lib/client'
import { message } from '../lib/api'
import type { components } from '../lib/api.generated'
import { useScope } from '../lib/scope'
import { Button } from './ui/button'
import { Dialog } from './ui/dialog'
import { Input } from './ui/input'
import { Icon } from './icons'
import { RequestError } from './shared'

type Retained = components['schemas']['RetainedApplicationData']
export function RetainedStorage({
  project,
  environment,
}: {
  project: string
  environment: string
}) {
  const scope = useScope()
  const cache = useQueryClient()
  const canManage =
    scope.identity.admin ||
    scope.identity.can_manage_applications ||
    scope.identity.project_roles?.some((role) => role.project === project && role.role === 'admin')
  const [selected, setSelected] = useState<Retained | null>(null)
  const [confirmation, setConfirmation] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const query = useQuery({
    queryKey: ['retained-storage', project, environment],
    enabled: Boolean(canManage),
    queryFn: ({ signal }) =>
      unwrap(
        client.GET('/storage/retained', { signal, params: { query: { project, environment } } }),
      ),
    refetchInterval: 15000,
    retry: false,
  })
  if (!canManage || (!query.error && !query.data?.items.length)) return null
  async function remove() {
    if (!selected || busy || confirmation !== selected.name) return
    setBusy(true)
    setError('')
    try {
      await unwrap(
        client.DELETE('/storage/retained/{id}', {
          params: { path: { id: selected.application_id } },
          body: { confirm_name: confirmation },
        }),
      )
      await cache.invalidateQueries({ queryKey: ['retained-storage', project, environment] })
      setSelected(null)
    } catch (e) {
      setError(message(e))
    } finally {
      setBusy(false)
    }
  }
  return (
    <section className="grid min-w-0 grid-cols-1 gap-3 py-4" aria-label="Retained application data">
      <h2 className="text-sm font-medium">Retained application data</h2>
      <p className="text-sm text-muted-foreground">
        Deleted applications can leave persistent data behind. Reclaim it here when you no longer
        need it. Saved backups are kept.
      </p>
      {query.error && <RequestError error={message(query.error)} />}
      {query.data?.items.map((item) => (
        <div
          key={item.application_id}
          className="flex min-w-0 flex-wrap items-center justify-between gap-3 border-b border-border py-3"
        >
          <div className="min-w-0 flex-1">
            <p className="break-all font-medium">{item.name}</p>
            <p className="text-xs text-muted-foreground">
              {item.reserved_gib > 0 ? `${item.reserved_gib} GiB storage reserved · ` : ''}
              {item.status === 'deleting' ? 'Reclaiming data' : 'Data retained'}
            </p>
            {item.error && <RequestError error={item.error} />}
          </div>
          <Button
            variant="danger"
            disabled={item.status === 'deleting' && !item.error}
            onClick={() => {
              setSelected(item)
              setConfirmation('')
              setError('')
            }}
          >
            <Icon name="trash" size={14} />
            {item.error ? 'Retry reclamation' : 'Reclaim data'}
          </Button>
        </div>
      ))}
      <Dialog
        className="wrap-anywhere"
        open={Boolean(selected)}
        onOpenChange={(open) => {
          if (!open && !busy) setSelected(null)
        }}
        title="Permanently delete retained data?"
        description="This deletes the application's retained volumes and native secrets. Database files cannot be recovered without a backup. Storage quota is released only after disk reclamation is verified. Existing backup archives are kept."
      >
        <form
          onSubmit={(event) => {
            event.preventDefault()
            void remove()
          }}
        >
          <div className="dialog-body field-stack">
            <p className="break-all">
              {selected?.project} / {selected?.environment}
            </p>
            <label>
              Type <strong className="break-all">{selected?.name}</strong> to confirm
              <Input
                aria-label="Confirm deleted application name"
                value={confirmation}
                onChange={(event) => setConfirmation(event.target.value)}
                disabled={busy}
                autoComplete="off"
              />
            </label>
            {error && <RequestError error={error} />}
          </div>
          <div className="dialog-footer">
            <Button type="button" disabled={busy} onClick={() => setSelected(null)}>
              Cancel
            </Button>
            <Button
              type="submit"
              variant="danger"
              disabled={busy || confirmation !== selected?.name}
            >
              {busy ? 'Requesting…' : 'Permanently delete data'}
            </Button>
          </div>
        </form>
      </Dialog>
    </section>
  )
}
