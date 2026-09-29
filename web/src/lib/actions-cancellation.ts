import type { components } from './api.generated'
import type { Application } from './types'
import { client, unwrap } from './client'

type Job = components['schemas']['ActionsJob']

export type NativeCancellationTarget = {
  applicationID: string
  applicationName: string
  project: string
  environment: string
  service: string
  slot: string
  repository: string
  pipelineID: string
  jobID: string
  runnerID: string
  name: string
}

export function nativeCancellationTarget(application: Application, service: string, item: Job) {
  const job = item.native_job
  if (
    item.provider !== 'gitlab' ||
    item.can_cancel !== true ||
    !job ||
    !['queued', 'in_progress'].includes(job.status)
  )
    return null
  return {
    applicationID: application.id,
    applicationName: application.name,
    project: application.project,
    environment: application.environment,
    service,
    slot: item.slot_id,
    repository: job.identity.repository,
    pipelineID: job.identity.run_id,
    jobID: job.identity.job_id,
    runnerID: job.identity.runner_id,
    name: job.name,
  } satisfies NativeCancellationTarget
}

export function nativeCancellationBlocked(
  review: NativeCancellationTarget,
  current: NativeCancellationTarget | null,
  permitted: boolean,
) {
  if (!permitted) return 'You no longer have permission to cancel this job.'
  if (!current) return 'This job is no longer available for cancellation. Refresh its status.'
  if (
    Object.keys(review).some(
      (key) =>
        review[key as keyof NativeCancellationTarget] !==
        current[key as keyof NativeCancellationTarget],
    )
  ) {
    return 'The selected job changed. Close this review and check the current job.'
  }
  return ''
}

type CancelResponse = { status: string; scope: 'job' }
type CancelRequest = (
  target: NativeCancellationTarget,
  signal: AbortSignal,
) => Promise<CancelResponse>

const sendCancellation: CancelRequest = (target, signal) =>
  unwrap(
    client.POST('/applications/{id}/actions/{service}/jobs/{slot}/cancel', {
      signal,
      params: { path: { id: target.applicationID, service: target.service, slot: target.slot } },
    }),
  )

export async function requestNativeJobCancellation(
  review: NativeCancellationTarget,
  current: NativeCancellationTarget | null,
  permitted: boolean,
  signal: AbortSignal,
  send: CancelRequest = sendCancellation,
) {
  const blocked = nativeCancellationBlocked(review, current, permitted)
  if (blocked) throw new Error(blocked)
  const result = await send(review, signal)
  if (result.status !== 'cancellation_requested' || result.scope !== 'job') {
    throw new Error('Cancellation was not confirmed. Refresh the job before retrying.')
  }
  return result
}
