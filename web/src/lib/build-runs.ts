import type { components } from './api.generated'

type RunObservation = Partial<
  Pick<components['schemas']['BuildRun'], 'status' | 'conclusion' | 'image' | 'automatic' | 'auto_status'>
>

export function buildRunNeedsObservation(run?: RunObservation) {
  if (!run) return false
  if (run.automatic) {
    // Image verification can finish before the worker records deployment acceptance.
    if (run.auto_status === 'queued' || run.auto_status === 'processing') return true
    if (['ready', 'deployed', 'blocked', 'finished', 'superseded'].includes(run.auto_status || ''))
      return false
  }
  if (run.status === 'completed') return run.conclusion === 'success' && run.image === ''
  return [
    'dispatching',
    'dispatch_unknown',
    'queued',
    'requested',
    'waiting',
    'pending',
    'in_progress',
    'cancelling',
  ].includes(run.status || '')
}
