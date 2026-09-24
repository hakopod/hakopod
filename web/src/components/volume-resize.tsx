import { useEffect, useRef, useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import type { components } from '../lib/api.generated'
import type { Application } from '../lib/types'
import { client, unwrap } from '../lib/client'
import { message } from '../lib/api'
import { canAccess, useScope } from '../lib/scope'
import { Dialog } from './ui/dialog'
import { Input } from './ui/input'
import { Button } from './ui/button'
import { Icon } from './icons'
import { RequestError } from './shared'

type Resize = components['schemas']['VolumeResize']
type Plan = components['schemas']['VolumeResizePlan']
type Action = 'retry' | 'cancel' | 'retain' | 'delete-original'
const finished = new Set(['completed', 'cancelled'])
const phaseLabels: Record<string, string> = {
  queued: 'Waiting to start',
  copying: 'Checking and copying data',
  switching: 'Starting on the resized volume',
  original_retained: 'Original volume retained',
  cancelling: 'Restoring original services',
  cancelled: 'Resize cancelled',
  deleting_original: 'Reclaiming original storage',
  completed: 'Resize complete',
}
function useCanResize(application: Application) {
  const { identity } = useScope()
  return (
    canAccess(identity, application.project, 'deployments:write') &&
    Boolean(
      identity.admin ||
      identity.can_manage_applications ||
      identity.project_roles?.some(
        (role) => role.project === application.project && role.role === 'admin',
      ),
    )
  )
}
function useResizes(application: Application) {
  return useQuery({
    queryKey: ['volume-resizes', application.id],
    queryFn: ({ signal }) =>
      unwrap(
        client.GET('/applications/{id}/volume-resizes', {
          signal,
          params: { path: { id: application.id } },
        }),
      ),
    refetchInterval: (query) =>
      query.state.data?.items.some(
        (item) => !finished.has(item.phase) && item.phase !== 'original_retained' && !item.error,
      )
        ? 3000
        : false,
    staleTime: 3000,
  })
}

export function VolumeResizeButton({
  application,
  claim,
  size,
}: {
  application: Application
  claim: string
  size: number
}) {
  const allowed = useCanResize(application)
  const resizes = useResizes(application)
  const [open, setOpen] = useState(false)
  const active = resizes.data?.items.find(
    (item) => !finished.has(item.phase) && (item.claim === claim || item.target_claim === claim),
  )
  if (!allowed && !active) return null
  return (
    <>
      <Button
        size="sm"
        variant="outline"
        disabled={!active && (resizes.isPending || !!resizes.error)}
        onClick={() => setOpen(true)}
      >
        {active ? 'View resize' : resizes.isPending ? 'Checking storage…' : 'Resize volume'}
      </Button>
      {resizes.error && <RequestError error={resizes.error} />}
      {open && (
        <VolumeResizeDialog
          application={application}
          claim={claim}
          size={size}
          initial={active}
          onClose={() => setOpen(false)}
        />
      )}
    </>
  )
}

export function VolumeResizeOperations({ application }: { application: Application }) {
  const resizes = useResizes(application)
  const [selected, setSelected] = useState<Resize>()
  const active = resizes.data?.items.filter((item) => !finished.has(item.phase)) || []
  if (!active.length && !selected && !resizes.error) return null
  return (
    <section className="grid gap-2 py-3" aria-label="Volume maintenance">
      {resizes.error && <RequestError error={resizes.error} />}
      {active.map((item) => (
        <div key={item.id} className="flex flex-wrap items-center gap-2 text-sm">
          <span className="min-w-0 break-all">
            {item.claim}: {item.old_gib} → {item.size_gib} GiB
          </span>
          <span className="text-muted-foreground">
            {item.error ? 'Needs attention' : phaseLabels[item.phase] || item.phase}
          </span>
          <Button size="sm" variant="outline" onClick={() => setSelected(item)}>
            View resize
          </Button>
        </div>
      ))}
      {selected && (
        <VolumeResizeDialog
          application={application}
          claim={selected.claim}
          size={selected.old_gib}
          initial={selected}
          onClose={() => setSelected(undefined)}
        />
      )}
    </section>
  )
}

export function VolumeResizeDialog({
  application,
  claim,
  size,
  initial,
  onClose,
}: {
  application: Application
  claim: string
  size: number
  initial?: Resize
  onClose: () => void
}) {
  const [snapshot] = useState(application)
  const allowed = useCanResize(application)
  const [target, setTarget] = useState(String(size))
  const [plan, setPlan] = useState<Plan>()
  const [accepted, setAccepted] = useState<Resize | undefined>(initial)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [deleteConfirmed, setDeleteConfirmed] = useState(false)
  const summary = useRef<HTMLParagraphElement>(null)
  const lock = useRef(false)
  const key = useRef(crypto.randomUUID())
  const resizes = useResizes(application)
  const cache = useQueryClient()
  const operation = resizes.data?.items.find((item) => item.id === accepted?.id) || accepted
  const validSize =
    Number.isInteger(Number(target)) &&
    Number(target) >= 1 &&
    Number(target) <= 200 &&
    Number(target) !== size
  const stale =
    application.id !== snapshot.id || (!operation && application.revision !== snapshot.revision)
  const canDeleteOriginal =
    !!operation?.verified && application.status === 'healthy' && allowed && !stale && !resizes.error
  useEffect(() => {
    setDeleteConfirmed(false)
  }, [
    allowed,
    application.revision,
    application.status,
    operation?.id,
    operation?.phase,
    resizes.error,
  ])
  async function refresh() {
    await Promise.all([
      cache.invalidateQueries({ queryKey: ['volume-resizes', snapshot.id] }),
      cache.invalidateQueries({ queryKey: ['application', snapshot.id] }),
    ])
  }
  async function submit() {
    if (lock.current || !allowed || stale || !validSize || resizes.error || resizes.isPending)
      return
    lock.current = true
    setBusy(true)
    setError('')
    try {
      const params = { path: { id: snapshot.id }, header: { 'Idempotency-Key': key.current } }
      const body = { claim, size_gib: Number(target), expected_revision: snapshot.revision }
      if (!plan) {
        setPlan(
          await unwrap(client.POST('/applications/{id}/volume-resizes/plan', { params, body })),
        )
      } else {
        const result = await unwrap(
          client.POST('/applications/{id}/volume-resizes', {
            params,
            body: { ...body, source_uid: plan.source_uid, confirm_downtime: true },
          }),
        )
        setAccepted(result)
        await refresh()
      }
    } catch (err) {
      setError(message(err))
    } finally {
      setBusy(false)
      lock.current = false
    }
  }
  async function act(action: Action) {
    if (
      !operation ||
      lock.current ||
      !allowed ||
      stale ||
      !!resizes.error ||
      (action === 'delete-original' && (!deleteConfirmed || !canDeleteOriginal))
    )
      return
    lock.current = true
    setBusy(true)
    setError('')
    try {
      await unwrap(
        client.POST('/applications/{id}/volume-resizes/{resize}/{action}', {
          params: { path: { id: snapshot.id, resize: operation.id, action } },
          body: action === 'delete-original' ? { confirm_claim: operation.claim } : {},
        }),
      )
      setDeleteConfirmed(false)
      await refresh()
    } catch (err) {
      setError(message(err))
    } finally {
      setBusy(false)
      lock.current = false
    }
  }
  return (
    <Dialog
      open
      onOpenChange={(open) => {
        if (!open && !busy) onClose()
      }}
      onOpenAutoFocus={(event) => {
        if (operation) {
          event.preventDefault()
          summary.current?.focus()
        }
      }}
      className="[overflow-wrap:anywhere]"
      title="Resize volume"
      description="Move data to a larger or smaller volume during a reviewed service interruption."
    >
      <div className="dialog-body grid min-w-0 grid-cols-1 gap-4 text-sm">
        {!operation ? (
          <>
            <p className="break-all">
              <strong>{claim}</strong> · {size} GiB
            </p>
            <label className="grid min-w-0 grid-cols-1 gap-1">
              New size in GiB
              <Input
                type="number"
                min={1}
                max={200}
                step={1}
                value={target}
                disabled={busy || !!plan || !allowed}
                onChange={(event) => setTarget(event.target.value)}
              />
            </label>
            {!plan && (
              <p className="text-muted-foreground">
                Growth and shrinking use a new volume. Services stop while data is checked and
                copied. Data fit is verified offline before switching; files are never truncated.
              </p>
            )}
            {plan && (
              <>
                <p>
                  <strong>
                    {plan.old_gib} → {plan.size_gib} GiB.
                  </strong>{' '}
                  Stop: {plan.services.join(', ')}.
                </p>
                <p>
                  The copy needs {plan.temporary_gib} GiB of temporary storage. Both volumes total{' '}
                  {plan.peak_gib} GiB until you delete the original; other volumes also count toward
                  your quota.
                </p>
                {plan.warnings.map((warning) => (
                  <p key={warning}>{warning}</p>
                ))}
                <p className="text-muted-foreground">
                  Data usage is measured after services stop. If the data and required free space do
                  not fit, switching is blocked and the original services are asked to resume.
                  Cancel the resize to remove the staging copy and choose another size.
                </p>
              </>
            )}
            {stale && (
              <p role="status">
                The application changed. Close this dialog and review its current volume.
              </p>
            )}
          </>
        ) : (
          <>
            <p ref={summary} tabIndex={-1} className="break-all">
              <strong>{operation.claim}</strong> · {operation.old_gib} → {operation.size_gib} GiB
            </p>
            <p role="status">
              {operation.error
                ? 'Needs attention'
                : phaseLabels[operation.phase] || operation.phase}
            </p>
            <p>Affected services: {operation.services.join(', ')}.</p>
            {operation.verified && (
              <p>
                Filesystem copy verified: {(operation.copied_bytes / 1048576).toFixed(1)} MiB. Check
                your application before deleting the original.
              </p>
            )}
            {operation.error && <RequestError error={operation.error} />}
            {['queued', 'copying'].includes(operation.phase) && (
              <p>
                Cancel resize restores the original services and removes only the staging copy. The
                original volume and its data are kept.
              </p>
            )}
            {operation.phase === 'original_retained' && (
              <>
                <p>
                  The original {operation.old_gib} GiB volume is still retained and charged. The
                  application uses the resized volume. Changes written after switching exist only on
                  the new volume.
                </p>
                <p>
                  Save the new volume and mount settings from the application’s configuration in
                  your Git repository.
                </p>
                {allowed && (
                  <label className="flex items-start gap-2">
                    <input
                      type="checkbox"
                      checked={deleteConfirmed}
                      disabled={busy || !canDeleteOriginal}
                      onChange={(event) => setDeleteConfirmed(event.target.checked)}
                    />
                    <span className="min-w-0">
                      I checked the application and want to permanently delete {operation.claim},
                      reclaiming {operation.old_gib} GiB. Existing backups stay.
                    </span>
                  </label>
                )}
                {!operation.verified && (
                  <p>Copy verification is missing. The original volume cannot be deleted.</p>
                )}
                {application.status !== 'healthy' && (
                  <p>Wait for a healthy deployment before deleting the original volume.</p>
                )}
              </>
            )}
            {operation.phase === 'switching' && operation.error && (
              <p>
                Services may have written to the new volume. Keeping both ends maintenance so you
                can repair the current deployment; it does not roll data back.
              </p>
            )}
            {operation.phase === 'cancelled' && (
              <p>The original services are running, and staging storage was reclaimed.</p>
            )}
            {operation.phase === 'completed' && (
              <p>The original volume was reclaimed. The resized volume remains attached.</p>
            )}
          </>
        )}
        {!allowed && (
          <p role="status">
            Application-management and deployment-write permission are required for resize actions.
          </p>
        )}
        {error && <RequestError error={error} />}
        {resizes.error && <RequestError error={resizes.error} />}
      </div>
      <div className="dialog-footer flex-wrap">
        <Button disabled={busy} onClick={onClose}>
          Close
        </Button>
        {!operation && (
          <>
            {plan && (
              <Button disabled={busy} onClick={() => setPlan(undefined)}>
                Change size
              </Button>
            )}
            <Button
              variant="primary"
              disabled={
                busy || !allowed || stale || !validSize || !!resizes.error || resizes.isPending
              }
              onClick={() => void submit()}
            >
              {busy ? 'Working…' : plan ? 'Stop services and resize' : 'Review resize'}
            </Button>
          </>
        )}
        {operation && allowed && (
          <>
            {operation.error && !finished.has(operation.phase) && (
              <Button disabled={busy || stale || !!resizes.error} onClick={() => void act('retry')}>
                {busy
                  ? 'Working…'
                  : operation.phase === 'deleting_original'
                    ? 'Retry storage cleanup'
                    : operation.phase === 'cancelling'
                      ? 'Retry cancellation'
                      : 'Retry resize'}
              </Button>
            )}
            {['queued', 'copying'].includes(operation.phase) && (
              <Button
                disabled={busy || stale || !!resizes.error}
                onClick={() => void act('cancel')}
              >
                Cancel resize
              </Button>
            )}
            {operation.phase === 'switching' && operation.error && (
              <Button
                className="h-auto min-h-9 whitespace-normal"
                disabled={busy || stale || !!resizes.error}
                onClick={() => void act('retain')}
              >
                Keep both and end maintenance
              </Button>
            )}
            {operation.phase === 'original_retained' && (
              <Button
                variant="danger"
                disabled={busy || !deleteConfirmed || !canDeleteOriginal}
                onClick={() => void act('delete-original')}
              >
                <Icon name="trash" size={14} />
                {busy ? 'Requesting…' : 'Delete original volume'}
              </Button>
            )}
          </>
        )}
      </div>
    </Dialog>
  )
}
