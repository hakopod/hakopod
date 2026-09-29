import type { Service } from './types'
import { runnerReservation, type RunnerResources } from './runner-resources'

type Actions = NonNullable<Service['actions']>
export type RunnerBinding = {
  application: string
  service: string
  architecture: string
  image: string
  gitlab?: Actions['gitlab']
  bitbucket?: Actions['bitbucket']
  cache: boolean
  cross_architecture: boolean
}

function coordinator(value: string) {
  try {
    if (/[\\%?#\s]/.test(value)) return ''
    const url = new URL(value || 'https://gitlab.com')
    if (url.protocol !== 'https:' || url.username || url.password) return ''
    return `${url.origin}${url.pathname.replace(/\/+$/, '')}`
  } catch {
    return ''
  }
}

function uuid(value?: string) {
  const plain = value?.replace(/^\{(.*)\}$/, '$1').toLowerCase()
  return plain && /^[0-9a-f]{8}(?:-[0-9a-f]{4}){3}-[0-9a-f]{12}$/.test(plain) ? plain : ''
}

// Match the loaded application's scope and the complete provider target. A
// different pool's image or cache permission must never become a fallback.
export function runnerBinding(
  bindings: readonly RunnerBinding[] | undefined,
  selection: Pick<
    RunnerBinding,
    'application' | 'service' | 'architecture' | 'gitlab' | 'bitbucket'
  >,
) {
  if (!selection.architecture || !!selection.gitlab === !!selection.bitbucket) return undefined
  const matches = (bindings || []).filter((binding) => {
    if (
      binding.application !== selection.application ||
      binding.service !== selection.service ||
      binding.architecture !== selection.architecture
    )
      return false
    if (selection.gitlab && binding.gitlab && !binding.bitbucket) {
      const url = coordinator(selection.gitlab.url)
      return (
        !!url &&
        url === coordinator(binding.gitlab.url) &&
        (selection.gitlab.project_id || 0) === (binding.gitlab.project_id || 0) &&
        (selection.gitlab.group_id || 0) === (binding.gitlab.group_id || 0) &&
        (selection.gitlab.trust_policy || '') === (binding.gitlab.trust_policy || '')
      )
    }
    if (selection.bitbucket && binding.bitbucket && !binding.gitlab) {
      const workspace = uuid(selection.bitbucket.workspace),
        repository = uuid(selection.bitbucket.repository)
      return (
        !!workspace &&
        !!repository &&
        workspace === uuid(binding.bitbucket.workspace) &&
        repository === uuid(binding.bitbucket.repository)
      )
    }
    return false
  })
  return matches.length === 1 ? matches[0] : undefined
}

export function runnerResourcesMeetMinimum(resources: RunnerResources, minimum?: RunnerResources) {
  if (!minimum) return true
  const request = runnerReservation(resources, 1),
    floor = runnerReservation(minimum, 1)
  const limit = runnerReservation(
    { cpu_request: resources.cpu_limit, memory_request: resources.memory_limit },
    1,
  )
  const floorLimit = runnerReservation(
    { cpu_request: minimum.cpu_limit, memory_request: minimum.memory_limit },
    1,
  )
  return (
    !!request &&
    !!floor &&
    !!limit &&
    !!floorLimit &&
    request.cpu >= floor.cpu &&
    request.memoryMiB >= floor.memoryMiB &&
    limit.cpu >= floorLimit.cpu &&
    limit.memoryMiB >= floorLimit.memoryMiB &&
    limit.cpu >= request.cpu &&
    limit.memoryMiB >= request.memoryMiB
  )
}

export function withRunnerMinimum(
  resources: RunnerResources,
  minimum?: RunnerResources,
): RunnerResources {
  if (!minimum) return resources
  const result = { ...resources }
  for (const key of ['cpu_request', 'cpu_limit', 'memory_request', 'memory_limit'] as const) {
    const cpu = key.startsWith('cpu')
    const current = runnerReservation(
      { cpu_request: cpu ? resources[key] : '1', memory_request: cpu ? '1Gi' : resources[key] },
      1,
    )
    const floor = runnerReservation(
      { cpu_request: cpu ? minimum[key] : '1', memory_request: cpu ? '1Gi' : minimum[key] },
      1,
    )
    if (
      !current ||
      (floor && (cpu ? current.cpu < floor.cpu : current.memoryMiB < floor.memoryMiB))
    )
      result[key] = minimum[key]
  }
  return result
}
