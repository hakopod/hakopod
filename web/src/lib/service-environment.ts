export type EnvironmentRow = { id: string; name: string; value: string }
export type Environment = Record<string, string>

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
    if (
      /(^|_)(PASSWORD|PASSWD|TOKEN|SECRET|API_KEY|PRIVATE_KEY|ACCESS_KEY|ACCESS_KEY_ID|SECRET_KEY)$/i.test(
        row.name,
      )
    )
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
