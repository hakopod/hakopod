import {
  parseEnvironment,
  sensitiveEnvironment,
  splitEnvironment,
  type EnvironmentRow,
} from './service-environment'

export const MAX_ENV_FILE_BYTES = 512 * 1024

// Parse data only: never evaluate shell commands or expand environment references.
export function importDotenv(
  text: string,
  existing: EnvironmentRow[],
  allowSecrets = false,
): EnvironmentRow[] {
  if (new TextEncoder().encode(text).length > MAX_ENV_FILE_BYTES)
    throw new Error('Choose a .env file smaller than 512 KiB.')
  const lines = text
    .replace(/^\uFEFF/, '')
    .replace(/\r\n?/g, '\n')
    .split('\n')
  const imported: EnvironmentRow[] = []
  for (let index = 0; index < lines.length; index++) {
    const line = lines[index].trim()
    if (!line || line.startsWith('#')) continue
    const match = /^(?:export\s+)?([A-Za-z_][A-Za-z0-9_]*)\s*=\s*(.*)$/.exec(line)
    if (!match) throw new Error(`Invalid assignment on line ${index + 1}. Use NAME=value.`)
    const name = match[1]
    if (name.length > 128)
      throw new Error(`Variable name on line ${index + 1} exceeds 128 characters.`)
    let value = match[2]
    if (value.startsWith('"') || value.startsWith("'")) {
      const quote = value[0]
      let source = value.slice(1)
      let end = -1
      let i = 0
      for (;;) {
        for (; i < source.length; i++) {
          if (quote === '"' && source[i] === '\\') {
            i++
            continue
          }
          if (source[i] === quote) {
            end = i
            break
          }
        }
        if (end >= 0) break
        if (++index >= lines.length) throw new Error(`Unclosed quoted value for ${name}.`)
        source += '\n' + lines[index]
      }
      const tail = source.slice(end + 1).trim()
      if (tail && !tail.startsWith('#'))
        throw new Error(`Unexpected text after the quoted value for ${name}.`)
      value = source.slice(0, end)
      if (quote === '"')
        value = value.replace(
          /\\([nrt"\\])/g,
          (_, escaped: string) => ({ n: '\n', r: '\r', t: '\t', '"': '"', '\\': '\\' })[escaped]!,
        )
    } else {
      value = value.replace(/(^|[ \t])#.*$/, '').trimEnd()
    }
    imported.push({
      id: crypto.randomUUID(),
      name,
      value,
      ...(allowSecrets && sensitiveEnvironment(name, value) ? { secret: true } : {}),
    })
  }
  if (!imported.length) throw new Error('This file contains no environment variables.')
  // Validate the complete change before returning it; no partial imports or overwrites.
  const result = [...existing.filter((row) => row.name !== '' || row.value !== ''), ...imported]
  if (allowSecrets) splitEnvironment(result)
  else parseEnvironment(result, [])
  return result
}
