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

export function runtimeReplicaSummary(health: RuntimeHealth, job = false) {
  if (health.ready === undefined || health.desired === undefined)
    return health.observed ? 'Unavailable' : 'Not observed'
  if (health.status === 'sleeping') return 'Wakes on request'
  if (health.status === 'scheduled') return 'Scheduled'
  if (job && health.status === 'stopped') return 'Paused'
  if (health.status === 'completed') return 'Completed'
  if (health.status === 'running') return 'Running'
  if (job) return health.status === 'failed' ? 'Failed' : 'Not completed'
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
    ['ready', 'healthy', 'completed'].includes(state) &&
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
    else if (
      /\b(?:CrashLoopBackOff|OOMKilled)\b|container exited|job failed|job.*exceeded.*deadline/i.test(
        message,
      )
    )
      inspect = 'logs'
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
    status:
      state === 'sleeping' && counts.desired === 0
        ? 'sleeping'
        : state === 'scheduled'
          ? 'scheduled'
          : state === 'stopped'
            ? 'stopped'
            : failed
              ? 'failed'
              : inspect
                ? 'blocked'
                : ready
                  ? state === 'completed'
                    ? 'completed'
                    : counts.desired === 0
                      ? 'scaled down'
                      : 'ready'
                  : state === 'missing'
                    ? 'missing'
                    : ['deploying', 'progressing', 'terminating', 'running'].includes(state)
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
  if (application && names.length === 0 && application.observed.status === 'empty')
    return observationTime(
      {
        status: 'empty',
        observed: true,
        ready: 0,
        desired: 0,
        issues: [],
        note: 'All services have been removed. Persistent storage is retained.',
      },
      application.observed.observed_at,
      now,
    )
  const observations = new Map(
    application?.observed?.services?.map((service) => [service.name, service]),
  )
  const services = names.map((name) => assessService(observations.get(name)))
  const observed = services.filter((service) => service.observed).length
  const ready = services.filter((service) =>
    ['ready', 'completed', 'stopped', 'scheduled', 'sleeping'].includes(service.status),
  ).length
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
      status:
        services.length > 0 && services.every((service) => service.status === 'sleeping')
          ? 'sleeping'
          : services.length > 0 && services.every((service) => service.status === 'stopped')
            ? 'stopped'
            : !observed
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
