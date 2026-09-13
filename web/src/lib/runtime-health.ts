import type { Application, ServiceStatus } from './types'

export const runtimeObservationMaxAge = 120_000

export type RuntimeIssue = {
  service: string
  message: string
  inspect: 'nodes' | 'pods' | 'logs'
}

export type RuntimeHealth = {
  status: string
  observed: boolean
  ready?: number
  desired?: number
  issues: RuntimeIssue[]
  note?: string
  observedAt?: string
}

export function runtimeReplicaSummary(health: RuntimeHealth) {
  if (health.ready === undefined || health.desired === undefined)
    return health.observed ? 'Unavailable' : 'Not observed'
  return `${health.ready} / ${health.desired} ready`
}

function assessService(service?: ServiceStatus): RuntimeHealth {
  if (!service) return { status: 'not observed', observed: false, issues: [] }
  const counts =
    Number.isInteger(service.ready) &&
    service.ready >= 0 &&
    Number.isInteger(service.desired) &&
    service.desired >= 0
      ? { ready: service.ready, desired: service.desired }
      : {}
  const state = service.status.toLowerCase()
  const ready =
    ['ready', 'healthy'].includes(state) &&
    counts.ready !== undefined &&
    counts.desired !== undefined &&
    counts.ready >= counts.desired
  const message = service.message?.trim()
  let inspect: RuntimeIssue['inspect'] | undefined
  if (!ready && message) {
    if (
      /\b(?:Unschedulable|FailedScheduling)\b|untolerated taint|Insufficient (?:cpu|memory)/i.test(
        message,
      )
    )
      inspect = 'nodes'
    else if (/\b(?:CrashLoopBackOff|OOMKilled)\b|container exited/i.test(message)) inspect = 'logs'
    else if (
      /\b(?:ImagePullBackOff|ErrImagePull|InvalidImageName|CreateContainerConfigError|CreateContainerError|RunContainerError|ProgressDeadlineExceeded)\b|readiness check has not passed/i.test(
        message,
      )
    )
      inspect = 'pods'
  }
  const failed = ['failed', 'error', 'unhealthy', 'crashed', 'crashloop'].includes(state)
  return {
    observed: true,
    ...counts,
    status: inspect
      ? 'blocked'
      : failed
        ? 'failed'
        : ready
          ? counts.desired === 0
            ? 'scaled down'
            : 'ready'
          : state === 'missing'
            ? 'missing'
            : ['deploying', 'progressing', 'terminating'].includes(state)
              ? state
              : 'pending',
    issues:
      inspect || failed
        ? [
            {
              service: service.name,
              message:
                message || 'The service has not reached readiness. Inspect its pods for the cause.',
              inspect: inspect || 'pods',
            },
          ]
        : [],
  }
}

function observationTime(
  health: RuntimeHealth,
  observedAt?: string,
  now = Date.now(),
): RuntimeHealth {
  if (!health.observed) return { ...health, note: 'Current pod health has not been observed.' }
  const time = observedAt ? Date.parse(observedAt) : NaN
  if (!Number.isFinite(time) || time > now + 60_000)
    return {
      ...health,
      status: 'unknown',
      ready: undefined,
      desired: undefined,
      issues: [],
      note: 'The observation time is unavailable. Current health is unknown.',
    }
  if (now - time > runtimeObservationMaxAge)
    return {
      ...health,
      status: 'stale',
      ready: undefined,
      desired: undefined,
      issues: [],
      observedAt,
      note: 'The last observation is over two minutes old. Current health is unknown.',
    }
  return { ...health, observedAt }
}

export function serviceRuntimeHealth(
  service?: ServiceStatus,
  observedAt?: string,
  now = Date.now(),
) {
  return observationTime(assessService(service), observedAt, now)
}

// Deployment outcomes and the persisted application status are deliberately not health inputs.
export function applicationRuntimeHealth(
  application?: Pick<Application, 'spec' | 'observed'>,
  now = Date.now(),
): RuntimeHealth {
  const names = Object.keys(application?.spec.services || {})
  const observations = new Map(
    application?.observed?.services?.map((service) => [service.name, service]),
  )
  const services = names.map((name) => assessService(observations.get(name)))
  const observed = services.filter((service) => service.observed).length
  const ready = services.filter((service) => service.status === 'ready').length
  const counts =
    services.length &&
    services.every((service) => service.ready !== undefined && service.desired !== undefined)
      ? {
          ready: services.reduce((total, service) => total + service.ready!, 0),
          desired: services.reduce((total, service) => total + service.desired!, 0),
        }
      : {}
  return observationTime(
    {
      observed: observed > 0,
      ...counts,
      status: !observed
        ? 'not observed'
        : services.some((service) => service.status === 'failed')
          ? 'failed'
          : services.some((service) => service.status === 'blocked')
            ? 'blocked'
            : ready === names.length
              ? 'healthy'
              : services.every((service) => service.status === 'scaled down')
                ? 'scaled down'
                : ready || observed < names.length
                  ? 'partial'
                  : 'pending',
      issues: services.flatMap((service) => service.issues),
      ...(observed > 0 && observed < names.length
        ? {
            note: `${names.length - observed} of ${names.length} services have no runtime observation.`,
          }
        : {}),
    },
    application?.observed?.observed_at,
    now,
  )
}

export function currentDeploymentRuntime(
  application: Application | undefined,
  revision: number,
  now = Date.now(),
) {
  return application?.revision === revision ? applicationRuntimeHealth(application, now) : undefined
}
