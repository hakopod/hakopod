import type { components } from './api.generated'

export type PlatformMigration = components['schemas']['PlatformMigration']
export type MigrationProposal = components['schemas']['MigrationProposal']
export type MigrationStep = components['schemas']['MigrationStep']
export type MigrationSourceProject = components['schemas']['MigrationSourceProject']
export type MigrationProvider = PlatformMigration['provider']
// Hakopod service name → variable name → value.
export type MigrationValues = Record<string, Record<string, string>>

export const providerNames: Record<MigrationProvider, string> = {
  railway: 'Railway',
  vercel: 'Vercel',
}

export const stepNames: Record<MigrationStep['name'], string> = {
  secrets: 'Secrets',
  databases: 'Databases',
  data: 'Data copy',
  deploy: 'First deployment',
  builds: 'Builds',
  domains: 'Domains',
  finish: 'Finish',
}

// Polling stops once the worker can no longer change the record.
export const migrationActive = (status?: PlatformMigration['status']) =>
  status === 'queued' || status === 'running'

// A Hakopod application name from a source project name, e.g. "My Shop API" → "my-shop-api".
export function applicationNameFrom(name: string) {
  return name
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, '-')
    .replace(/^[^a-z]+/, '')
    .slice(0, 40)
    .replace(/-+$/, '')
}

// Railway projects default to their production environment.
export function defaultEnvironment(project?: MigrationSourceProject) {
  const environments = project?.environments || []
  return (environments.find((item) => item.name.toLowerCase() === 'production') || environments[0])
    ?.id
}

// Variables still without a value, counting entered values as filled.
export function stillMissing(proposal: MigrationProposal, values: MigrationValues = {}) {
  return proposal.missing.filter((item) => !values[item.service]?.[item.name])
}

// Only non-empty values are sent; an empty field is not a value.
export function filledValues(values: MigrationValues) {
  const out: MigrationValues = {}
  for (const [service, variables] of Object.entries(values))
    for (const [name, value] of Object.entries(variables))
      if (value) (out[service] ||= {})[name] = value
  return out
}
