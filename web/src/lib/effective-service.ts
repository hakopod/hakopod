import type { Service, Spec } from './types'

export function effectiveService(app: Spec, service: Service): Service {
  const env = Object.assign(
    Object.create(null) as Record<string, string>,
    app.inject_env ? app.env : {},
    service.env,
  )
  const secrets = Object.assign(Object.create(null) as NonNullable<Service['secrets']>, app.secrets)
  for (const key of Object.keys(service.env || {})) delete secrets[key]
  Object.assign(secrets, service.secrets)
  for (const key of Object.keys(secrets)) delete env[key]
  for (const key of Object.keys(service.bindings || {})) {
    delete env[key]
    delete secrets[key]
  }
  return { ...service, env, secrets }
}
