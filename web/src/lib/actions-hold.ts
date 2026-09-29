import type { components } from './api.generated'
import { client, unwrap } from './client'

export type ActionsHold = components['schemas']['ActionsProviderHold']
export type ActionsHoldState = { hold: ActionsHold | null; active_slots: number }
export type HoldReview = {
  applicationID: string
  applicationName: string
  project: string
  environment: string
  service: string
  hold: ActionsHold
}

export function holdReleaseBlocked(
  review: HoldReview,
  current: ActionsHoldState | undefined,
  permitted: boolean,
  acknowledged: boolean,
) {
  if (!permitted) return 'You do not have permission to release this hold.'
  if (!current) return 'Refresh the hold before releasing it.'
  if (!current.hold || current.hold.id !== review.hold.id)
    return 'This hold changed. Close this review and inspect the current hold.'
  if (current.active_slots !== 0)
    return 'Wait for all runner registrations to finish cleanup before releasing this hold.'
  if (!acknowledged)
    return 'Confirm that you reviewed the jobs and corrected the cause of runner reuse.'
  return ''
}

type ReleaseResponse = { status: 'released'; hold_id: string }
type ReleaseRequest = (review: HoldReview, signal: AbortSignal) => Promise<ReleaseResponse>
const sendRelease: ReleaseRequest = (review, signal) =>
  unwrap(
    client.POST('/applications/{id}/actions/{service}/hold/release', {
      signal,
      params: { path: { id: review.applicationID, service: review.service } },
      body: { hold_id: review.hold.id, acknowledge: true },
    }),
  )

export async function requestHoldRelease(
  review: HoldReview,
  current: ActionsHoldState | undefined,
  permitted: boolean,
  acknowledged: boolean,
  signal: AbortSignal,
  send: ReleaseRequest = sendRelease,
) {
  const blocked = holdReleaseBlocked(review, current, permitted, acknowledged)
  if (blocked) throw new Error(blocked)
  const result = await send(review, signal)
  if (result.status !== 'released' || result.hold_id !== review.hold.id)
    throw new Error('Hold release was not confirmed. Refresh the pool before retrying.')
  return result
}
