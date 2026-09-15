import type { Service } from './types'

export function readinessLabel(service: Service): string {
  if (service.job?.schedule)
    return `${service.job.schedule.cron} · ${service.job.schedule.timezone || 'UTC'}`
  if (service.job) return 'Job completion'
  const check = service.readiness
  if (!check) return service.healthcheck || (service.port ? 'TCP probe' : 'Process health')
  const protocol =
    check.protocol === 'smtp_starttls' ? 'SMTP STARTTLS' : check.protocol.toUpperCase()
  const listener = `${protocol} :${check.port}`
  return service.healthcheck ? `${service.healthcheck} + ${listener}` : listener
}
