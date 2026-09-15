import { client, unwrap } from './client'
import { splitEnvironment, type EnvironmentRow } from './service-environment'
import type { Service } from './types'

// Imported secret values are written only to new, application-scoped references.
// They never enter a spec, build configuration, workflow or deployment history.
export async function saveEnvironment(
  rows: EnvironmentRow[],
  query: { project: string; environment: string; application: string },
  existing: Service['secrets'] = {},
  signal?: AbortSignal,
) {
  const reference = (id: string) => `env-${id.replaceAll('-', '').slice(0, 32)}`
  const existingNames = Object.keys(existing).filter(
    (name) => !rows.some((row) => row.name === name && existing[name].ref === reference(row.id)),
  )
  const parsed = splitEnvironment(rows, existingNames)
  const secrets = Object.assign(Object.create(null) as NonNullable<Service['secrets']>, existing)
  if (new Set([...Object.keys(secrets), ...parsed.secrets.map((row) => row.name)]).size > 32)
    throw new Error('At most 32 secret references are supported.')
  if (!/^[a-z](?:[a-z0-9-]{0,38}[a-z0-9])?$/.test(query.application))
    throw new Error('Enter a valid application name before saving variables.')
  for (const row of parsed.secrets) {
    const ref = reference(row.id)
    await unwrap(
      client.PUT('/secrets/{name}', {
        signal,
        params: { path: { name: ref }, query },
        body: { value: row.value },
      }),
    )
    secrets[row.name] = { ref }
  }
  return { env: parsed.env, secrets }
}
