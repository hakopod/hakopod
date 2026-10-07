import type { components } from './api.generated'
import type { DatabaseSpec } from './databases'
import { client, unwrap } from './client'

export type DatabaseBindingOptions = Pick<components['schemas']['ServiceBinding'], 'username' | 'database' | 'password' | 'ssl_mode'>
export type DatabaseBindingSSLMode = NonNullable<DatabaseBindingOptions['ssl_mode']>
export type DatabaseBindingDraft = {
  username: string
  database: string
  sslMode: DatabaseBindingSSLMode
  passwordSource: 'managed' | 'existing' | 'enter'
  passwordRef: string
  passwordValue: string
}
export const emptyDatabaseBindingDraft: DatabaseBindingDraft = {
  username: '', database: '', sslMode: '', passwordSource: 'managed', passwordRef: '', passwordValue: '',
}

export function managedDatabaseUser(engine: DatabaseSpec['engine'], endpoint = '') {
  return engine === 'duckdb' ? endpoint === 'postgresql' ? 'postgres' : 'root' : engine === 'redis' ? 'default' : engine === 'oracle' ? 'APP' : 'app'
}

export function managedDatabaseName(spec: DatabaseSpec) {
  return spec.engine === 'redis' ? '0' : spec.engine === 'oracle' ? spec.oracle?.edition === 'enterprise' ? 'APPDB' : 'FREEPDB1' : 'app'
}

export function databaseBindingDatabaseLabel(engine: DatabaseSpec['engine']) {
  return engine === 'redis' ? 'Database index' : engine === 'oracle' ? 'Service name' : engine === 'vitess' ? 'Keyspace' : 'Database name'
}

export function databaseBindingSSLOptions(spec: DatabaseSpec, endpoint = '') {
  const driverTLS = spec.engine === 'mysql' || spec.engine === 'vitess' || spec.engine === 'duckdb' && endpoint !== 'postgresql'
  const required = spec.tls?.mode === 'required'
  const options: { value: DatabaseBindingSSLMode; label: string }[] = [{ value: '', label: `Database policy (${required ? driverTLS ? 'TLS required' : 'verify-full' : 'legacy plaintext'})` }]
  if (driverTLS) return options
  if (!required) {
    if (spec.engine === 'postgresql' || spec.engine === 'redis') options.push({ value: 'disable', label: 'Disable TLS (legacy database)' })
    return options
  }
  if (spec.engine === 'postgresql') options.push(
    { value: 'require', label: 'Require encryption (require)' },
    { value: 'verify-ca', label: 'Verify certificate authority (verify-ca)' },
  )
  options.push({ value: 'verify-full', label: 'Verify certificate and hostname (verify-full)' })
  return options
}

export function databaseBindingIssue(spec: DatabaseSpec, draft: DatabaseBindingDraft, endpoint = '') {
  if (new TextEncoder().encode(draft.username).length > 64 || /[\x00\r\n]/.test(draft.username))
    return 'Use a username up to 64 bytes without line breaks.'
  if (new TextEncoder().encode(draft.database).length > 64 || /[/\\?#\x00\r\n]/.test(draft.database))
    return 'Use a database name up to 64 bytes without URL separators or line breaks.'
  if (spec.engine === 'redis' && draft.database && (!/^(?:[0-9]|1[0-5])$/.test(draft.database) || spec.mode === 'cluster' && draft.database !== '0'))
    return spec.mode === 'cluster' ? 'Redis Cluster supports only database 0.' : 'Choose a Redis database index from 0 to 15.'
  if (spec.engine === 'vitess' && draft.database.includes('@'))
    return 'Enter a keyspace without a route suffix; the endpoint selects primary or replica routing.'
  if (spec.engine === 'vitess' && (draft.username && draft.username !== 'app' || draft.database && draft.database !== 'app'))
    return 'This managed Vitess instance supports only the app login and keyspace. Leave these fields blank or use app.'
  const myduckUser = endpoint === 'postgresql' ? 'postgres' : 'root'
  if (spec.engine === 'duckdb' && (draft.username && draft.username !== myduckUser || draft.database && draft.database !== 'app' || draft.passwordSource !== 'managed'))
    return `DuckDB (MyDuck) currently supports only its managed ${myduckUser} user, app database and managed password for this protocol.`
  if (spec.engine === 'postgresql' && endpoint.startsWith('pooled_') && (draft.username && draft.username !== 'app' || draft.database && draft.database !== 'app'))
    return 'Choose a direct endpoint for a custom login or database.'
  if (!databaseBindingSSLOptions(spec, endpoint).some((option) => option.value === draft.sslMode))
    return 'Choose an SSL mode supported by this database and its TLS policy.'
  if (draft.username && draft.username !== managedDatabaseUser(spec.engine, endpoint) && draft.passwordSource === 'managed')
    return 'Provide the existing password for this username, or select a saved application secret.'
  if (draft.passwordSource === 'existing' && !draft.passwordRef)
    return 'Choose a saved password from this application’s secrets.'
  if (draft.passwordSource === 'enter' && (!draft.passwordValue || new TextEncoder().encode(draft.passwordValue).length > 4096 || /[\x00\r\n]/.test(draft.passwordValue)))
    return 'Enter the existing password without line breaks, up to 4096 bytes.'
}

// Raw values stay in the form. The plan and application spec receive only a saved reference.
export function databaseBindingOptions(draft: DatabaseBindingDraft): DatabaseBindingOptions {
  if (draft.passwordSource === 'enter') throw new Error('Save the password before reviewing the connection.')
  return {
    ...(draft.username ? { username: draft.username } : {}),
    ...(draft.database ? { database: draft.database } : {}),
    ...(draft.sslMode ? { ssl_mode: draft.sslMode } : {}),
    ...(draft.passwordSource === 'existing' && draft.passwordRef ? { password: { ref: draft.passwordRef } } : {}),
  }
}

export async function saveDatabaseBindingPassword(value: string, scope: { project: string; environment: string; application: string }) {
  if (!scope.project || !scope.environment || !scope.application) throw new Error('Choose an application before saving its password.')
  const name = `db-${crypto.randomUUID().replaceAll('-', '')}`
  const saved = await unwrap(client.POST('/secrets/{name}', {
    params: { path: { name }, query: scope }, body: { value },
  }))
  if (!saved.saved || saved.name !== name) throw new Error('The password save could not be confirmed. Try again.')
  return name
}

export function databaseConnectionReviewIssue(
  plan: components['schemas']['DatabaseConnectionPlan'] | null,
  databaseRevision: number,
  applicationRevision: number | undefined,
  now: number,
) {
  if (!plan) return
  if (!Number.isFinite(Date.parse(plan.expires_at)) || Date.parse(plan.expires_at) <= now)
    return 'This review expired. Review the connection again.'
  if (plan.database_revision !== databaseRevision || plan.application_revision !== applicationRevision)
    return 'The database or application changed. Review the connection again.'
}
