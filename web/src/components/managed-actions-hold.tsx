import { useEffect, useRef, useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import type { Application } from '../lib/types'
import { client, unwrap } from '../lib/client'
import { message } from '../lib/api'
import { canAccess, useScope } from '../lib/scope'
import {
  holdReleaseBlocked,
  requestHoldRelease,
  type ActionsHoldState,
  type HoldReview,
} from '../lib/actions-hold'
import { Button } from './ui/button'
import { Dialog } from './ui/dialog'
import { ErrorState, Note } from './shared'

export function ProviderHoldReview({
  review,
  current,
  permitted,
  acknowledged,
  onAcknowledge,
  busy,
  refreshing,
  error,
  onRefresh,
  onClose,
  onRelease,
}: {
  review: HoldReview
  current: ActionsHoldState | undefined
  permitted: boolean
  acknowledged: boolean
  onAcknowledge: (value: boolean) => void
  busy: boolean
  refreshing: boolean
  error: string
  onRefresh: () => void
  onClose: () => void
  onRelease: () => void
}) {
  const blocked = holdReleaseBlocked(review, current, permitted, acknowledged)
  return (
    <>
      <div className="dialog-body grid min-w-0 gap-4">
        <dl className="grid grid-cols-[auto_minmax(0,1fr)] gap-x-4 gap-y-2 text-sm">
          <dt>Pool</dt>
          <dd className="wrap-anywhere">
            {review.applicationName} / {review.service}
          </dd>
          <dt>Scope</dt>
          <dd className="wrap-anywhere">
            {review.project} / {review.environment}
          </dd>
          <dt>GitLab instance</dt>
          <dd className="wrap-anywhere">{review.hold.instance_url}</dd>
          <dt>Runner</dt>
          <dd className="wrap-anywhere">hakopod-{review.hold.slot_id}</dd>
          <dt>Recorded</dt>
          <dd className="wrap-anywhere">{new Date(review.hold.observed_at).toLocaleString()}</dd>
        </dl>
        <div className="grid gap-2">
          <h3 className="text-sm font-semibold">Recorded jobs</h3>
          <ul className="grid gap-2" aria-label="Jobs recorded for this hold">
            {review.hold.jobs.map((job) => (
              <li
                key={`${job.identity.repository}:${job.identity.job_id}`}
                className="grid gap-1 border border-border p-3 text-sm"
              >
                <strong className="wrap-anywhere">
                  {job.name} · Job {job.identity.job_id}
                </strong>
                <span className="wrap-anywhere">
                  Project {job.identity.repository} · Pipeline {job.identity.run_id}
                </span>
                <span className="wrap-anywhere">
                  {(job.conclusion || job.status).replaceAll('_', ' ')}
                </span>
              </li>
            ))}
          </ul>
        </div>
        <p className="text-sm">
          Releasing this hold allows the pool to start new runners. Correct the cause of runner
          reuse before continuing.
        </p>
        {current && (
          <p className="text-sm">
            {current.active_slots} runner slots remain. Cleanup must finish before release.
          </p>
        )}
        {permitted && (
          <label className="flex items-start gap-2 text-sm">
            <input
              type="checkbox"
              checked={acknowledged}
              disabled={busy || refreshing}
              onChange={(event) => onAcknowledge(event.target.checked)}
            />
            <span>I reviewed both jobs and corrected the cause of runner reuse.</span>
          </label>
        )}
        {blocked && <Note>{blocked}</Note>}
        {error && (
          <p className="text-sm text-destructive" role="alert">
            {error}
          </p>
        )}
      </div>
      <div className="dialog-footer flex-wrap">
        <Button disabled={busy || refreshing} onClick={onRefresh}>
          Refresh status
        </Button>
        <Button disabled={busy} onClick={onClose}>
          Keep hold
        </Button>
        {permitted && (
          <Button
            variant="primary"
            disabled={busy || refreshing || Boolean(blocked)}
            onClick={onRelease}
          >
            {busy ? 'Releasing hold…' : 'Release hold'}
          </Button>
        )}
      </div>
    </>
  )
}

export function ManagedActionsHold({
  application,
  service,
}: {
  application: Application
  service: string
}) {
  const scope = useScope()
  const permitted = canAccess(scope.identity, application.project, 'deployments:write')
  const cache = useQueryClient()
  const query = useQuery({
    queryKey: ['actions-hold', application.id, service],
    queryFn: ({ signal }) =>
      unwrap(
        client.GET('/applications/{id}/actions/{service}/hold', {
          signal,
          params: { path: { id: application.id, service } },
        }),
      ),
    refetchInterval: 15000,
    refetchIntervalInBackground: false,
    gcTime: 0,
    retry: false,
  })
  const [review, setReview] = useState<HoldReview | null>(null)
  const [acknowledged, setAcknowledged] = useState(false)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const request = useRef<AbortController | null>(null)
  useEffect(() => () => request.current?.abort(), [])
  async function release() {
    if (!review || request.current || query.isFetching || query.isError) return
    const controller = new AbortController()
    request.current = controller
    setBusy(true)
    setError('')
    const timeout = setTimeout(() => controller.abort(), 25000)
    try {
      if (
        review.applicationID !== application.id ||
        review.service !== service ||
        review.project !== application.project ||
        review.environment !== application.environment
      )
        throw new Error(
          'The selected pool changed. Close this review and inspect the current pool.',
        )
      await requestHoldRelease(review, query.data, permitted, acknowledged, controller.signal)
      setReview(null)
      void cache.invalidateQueries({ queryKey: ['actions-hold', application.id, service] })
      void cache.invalidateQueries({ queryKey: ['managed-actions', application.id] })
      void cache.invalidateQueries({ queryKey: ['actions-jobs', application.id, service] })
    } catch (cause) {
      setError(
        controller.signal.aborted
          ? 'Hold release could not be confirmed. Refresh the pool before retrying.'
          : message(cause),
      )
    } finally {
      clearTimeout(timeout)
      request.current = null
      setBusy(false)
    }
  }
  return (
    <>
      {query.error && <ErrorState error={query.error} retry={() => void query.refetch()} />}
      {query.data?.hold && (
        <div className="flex flex-wrap items-center justify-between gap-2 text-sm">
          <span>Pool held for review · {query.data.active_slots} runner slots remaining</span>
          <Button
            size="sm"
            onClick={() => {
              const hold = query.data?.hold
              if (!hold) return
              setReview({
                applicationID: application.id,
                applicationName: application.name,
                project: application.project,
                environment: application.environment,
                service,
                hold,
              })
              setAcknowledged(false)
              setError('')
              void query.refetch()
            }}
          >
            Inspect hold
          </Button>
        </div>
      )}
      {review && (
        <Dialog
          open
          wide
          onOpenChange={(open) => {
            if (!open && !busy) setReview(null)
          }}
          title="Review runner reuse"
          description="A one-job runner processed multiple jobs. Inspect the recorded evidence before releasing the pool."
        >
          <ProviderHoldReview
            review={review}
            current={query.isError ? undefined : query.data}
            permitted={permitted}
            acknowledged={acknowledged}
            onAcknowledge={setAcknowledged}
            busy={busy}
            refreshing={query.isFetching}
            error={error}
            onRefresh={() => void query.refetch()}
            onClose={() => setReview(null)}
            onRelease={() => void release()}
          />
        </Dialog>
      )}
    </>
  )
}
