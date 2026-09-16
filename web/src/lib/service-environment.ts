export type EnvironmentRow = { id: string; name: string; value: string; secret?: boolean }
export type Environment = Record<string, string>
export const MAX_SECRET_VALUE_BYTES = 64 * 1024

export const isSecretEnvironment = (row: EnvironmentRow) =>
  row.secret === true || sensitiveEnvironment(row.name, row.value)

const valueOf = (environment: Environment, name: string) =>
  Object.hasOwn(environment, name) ? environment[name] : undefined

export function environmentRows(environment: Environment = {}): EnvironmentRow[] {
  return Object.entries(environment)
    .sort(([a], [b]) => a.localeCompare(b))
    .map(([name, value]) => ({ id: crypto.randomUUID(), name, value }))
}

export function parseEnvironment(rows: EnvironmentRow[], secretNames: string[]): Environment {
  if (rows.length > 128) throw new Error('A service can have at most 128 plain variables.')
  const result: Environment = Object.create(null)
  for (const row of rows) {
    if (!/^[A-Za-z_][A-Za-z0-9_]*$/.test(row.name) || row.name.length > 128)
      throw new Error(
        'Variable names must start with a letter or underscore and contain only letters, numbers, and underscores (up to 128 characters).',
      )
    if (Object.hasOwn(result, row.name)) throw new Error(`${row.name} appears more than once.`)
    if (secretNames.includes(row.name))
      throw new Error(
        `${row.name} already uses a secret reference. Manage it in application secrets.`,
      )
    if (row.value.includes('\0') || new TextEncoder().encode(row.value).length > 4096)
      throw new Error(
        `${row.name} must be at most 4,096 bytes and cannot contain a null character.`,
      )
    if (isSecretEnvironment(row))
      throw new Error(`${row.name} belongs in application secrets, not plain variables.`)
    result[row.name] = row.value
  }
  return result
}

export function sameEnvironment(left: Environment = {}, right: Environment = {}) {
  const names = Object.keys(left)
  return (
    names.length === Object.keys(right).length &&
    names.every((name) => valueOf(left, name) === valueOf(right, name))
  )
}

export function mergeEnvironment(base: Environment, draft: Environment, current: Environment) {
  const environment: Environment = Object.assign(Object.create(null), current)
  const conflicts: string[] = []
  for (const name of new Set([...Object.keys(base), ...Object.keys(draft)])) {
    if (valueOf(base, name) === valueOf(draft, name)) continue
    if (
      valueOf(current, name) !== valueOf(base, name) &&
      valueOf(current, name) !== valueOf(draft, name)
    )
      conflicts.push(name)
    if (Object.hasOwn(draft, name)) environment[name] = draft[name]
    else delete environment[name]
  }
  return { environment, conflicts: conflicts.sort() }
}

export function environmentChanges(before: Environment = {}, after: Environment = {}) {
  return [...new Set([...Object.keys(before), ...Object.keys(after)])]
    .sort()
    .filter((name) => valueOf(before, name) !== valueOf(after, name))
    .map((name) => ({ name, before: valueOf(before, name), after: valueOf(after, name) }))
}

export function sensitiveEnvironment(name: string, value: string): boolean {
  if (
    /(^|_)(PASSWORD|PASSWD|PWD|TOKEN|SECRET|API_KEY|PRIVATE_KEY|ACCESS_KEY|ACCESS_KEY_ID|SECRET_KEY|CLIENT_SECRET|SIGNING_KEY|ENCRYPTION_KEY)$/i.test(
      name,
    ) ||
    /(^|_)(SECRET_KEY|PRIVATE_KEY|API_KEY|CLIENT_SECRET)(_|$)/i.test(name) ||
    /-----BEGIN (?:[A-Z0-9]+ )*PRIVATE KEY-----/.test(value) ||
    /^(?:gh[pousr]_|github_pat_|glpat-|sk-proj-)[A-Za-z0-9_-]{12,}/.test(value) ||
    /^eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+$/.test(value)
  )
    return true
  try {
    const url = new URL(value)
    return Boolean(url.username || url.password)
  } catch {
    return false
  }
}

export function splitEnvironment(rows: EnvironmentRow[], secretNames: string[] = []) {
  const plain: EnvironmentRow[] = [],
    secrets: EnvironmentRow[] = []
  const seen = new Set<string>()
  for (const row of rows.filter((row) => row.name !== '' || row.value !== '')) {
    if (seen.has(row.name)) throw new Error(`${row.name} appears more than once.`)
    seen.add(row.name)
    if (isSecretEnvironment(row)) {
      if (!/^[A-Za-z_][A-Za-z0-9_]{0,127}$/.test(row.name))
        throw new Error('Use a valid environment variable name.')
      if (
        !row.value ||
        row.value.includes('\0') ||
        new TextEncoder().encode(row.value).length > MAX_SECRET_VALUE_BYTES
      )
        throw new Error(`${row.name} must have a nonempty secret value up to 64 KiB without NUL.`)
      if (secretNames.includes(row.name))
        throw new Error(`${row.name} already has a secret reference. Replace its value in Secrets.`)
      secrets.push(row)
    } else plain.push(row)
  }
  if (secrets.length > 32) throw new Error('At most 32 secret variables can be imported.')
  return { env: parseEnvironment(plain, secretNames), secrets }
}
