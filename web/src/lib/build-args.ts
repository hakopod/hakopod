// Keep values verbatim: URLs, empty strings, and additional equals signs are valid.
export function formatBuildArgs(args: Record<string, string> = {}) {
  return Object.entries(args)
    .map(([key, value]) => `${key}=${value}`)
    .join('\n')
}

export function parseBuildArgs(text: string): Record<string, string> {
  const args: Record<string, string> = Object.create(null)
  for (const line of text.split(/\r?\n/)) {
    if (!line.trim()) continue
    const separator = line.indexOf('=')
    const key = line.slice(0, separator).trim()
    if (separator < 1 || !/^[A-Za-z_][A-Za-z0-9_]*$/.test(key))
      throw new Error('Use one public build value per line: NAME=value.')
    if (Object.hasOwn(args, key)) throw new Error(`${key} appears more than once.`)
    args[key] = line.slice(separator + 1)
  }
  return args
}
