import { useEffect, useRef, useState } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import type { components } from '../lib/api.generated'
import type { Application } from '../lib/types'
import { canAccess, useScope } from '../lib/scope'
import { message } from '../lib/api'
import {
  nativeCancellationBlocked,
  nativeCancellationTarget,
  requestNativeJobCancellation,
  type NativeCancellationTarget,
} from '../lib/actions-cancellation'
import { Button } from './ui/button'
import { Dialog } from './ui/dialog'
import { Note } from './shared'

export function NativeJobCancellationReview({
  target,
  blocked,
  error,
  busy,
  onClose,
  onConfirm,
}: {
  target: NativeCancellationTarget
  blocked: string
  error: string
  busy: boolean
  onClose: () => void
  onConfirm: () => void
}) {
  return (
    <>
      <div className="dialog-body grid min-w-0 gap-3">
        <dl className="grid grid-cols-[auto_minmax(0,1fr)] gap-x-4 gap-y-2 text-sm">
          <dt>Application</dt>
          <dd className="wrap-anywhere">
            {target.applicationName} / {target.service}
          </dd>
          <dt>Scope</dt>
          <dd className="wrap-anywhere">
            {target.project} / {target.environment}
          </dd>
          <dt>GitLab project</dt>
          <dd className="wrap-anywhere">{target.repository}</dd>
          <dt>Pipeline</dt>
          <dd className="wrap-anywhere">{target.pipelineID}</dd>
          <dt>Job</dt>
          <dd className="wrap-anywhere">
            {target.name} · {target.jobID}
          </dd>
        </dl>
        <p className="text-sm">
          GitLab will receive a cancellation request for this job. Its status may take a moment to
          update.
        </p>
        {blocked && <Note>{blocked}</Note>}
        {error && (
          <p className="text-sm text-destructive" role="alert">
            {error}
          </p>
        )}
      </div>
      <div className="dialog-footer">
        <Button disabled={busy} onClick={onClose}>
          Keep job
        </Button>
        <Button variant="danger" disabled={busy || Boolean(blocked)} onClick={onConfirm}>
          {busy ? 'Requesting cancellation…' : 'Cancel job'}
        </Button>
      </div>
    </>
  )
}

export function ManagedActionsJobCancel({
  application,
  service,
  item,
}: {
  application: Application
  service: string
  item: components['schemas']['ActionsJob']
}) {
  const scope = useScope()
  const permitted = canAccess(scope.identity, application.project, 'deployments:write')
  const current = nativeCancellationTarget(application, service, item)
  const [review, setReview] = useState<NativeCancellationTarget | null>(null)
  const [busy, setBusy] = useState(false)
  const [requested, setRequested] = useState(false)
  const [error, setError] = useState('')
  const request = useRef<AbortController | null>(null)
  const cache = useQueryClient()
  useEffect(() => () => request.current?.abort(), [])
  async function cancelJob() {
    if (!review || request.current || requested) return
    const controller = new AbortController()
    request.current = controller
    setBusy(true)
    setError('')
    const timeout = setTimeout(() => controller.abort(), 25000)
    try {
      await requestNativeJobCancellation(review, current, permitted, controller.signal)
      setRequested(true)
      setReview(null)
      void cache.invalidateQueries({ queryKey: ['actions-jobs', application.id, service] })
      void cache.invalidateQueries({
        queryKey: ['actions-job-logs', application.id, service, item.slot_id],
      })
    } catch (cause) {
      if (controller.signal.aborted)
        setError('Cancellation could not be confirmed. Refresh the job before retrying.')
      else setError(message(cause))
    } finally {
      clearTimeout(timeout)
      request.current = null
      setBusy(false)
    }
  }
  if (
    !review &&
    (!current || !permitted) &&
    (!requested || item.native_job?.status === 'completed')
  )
    return null
  return (
    <>
      <div className="flex flex-wrap items-center gap-2">
        {current && permitted && !requested && (
          <Button
            size="sm"
            variant="danger"
            onClick={() => {
              setError('')
              setReview(current)
            }}
          >
            Cancel job
          </Button>
        )}
        {requested && item.native_job?.status !== 'completed' && (
          <p className="text-sm" role="status">
            Cancellation requested. Waiting for GitLab to confirm the job status.
          </p>
        )}
      </div>
      {review && (
        <Dialog
          open
          onOpenChange={(open) => {
            if (!open && !busy) setReview(null)
          }}
          title="Cancel this job?"
          description="Review the GitLab job before requesting cancellation."
        >
          <NativeJobCancellationReview
            target={review}
            blocked={nativeCancellationBlocked(review, current, permitted)}
            error={error}
            busy={busy}
            onClose={() => setReview(null)}
            onConfirm={() => void cancelJob()}
          />
        </Dialog>
      )}
    </>
  )
}
