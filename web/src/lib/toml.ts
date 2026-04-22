import type { Spec } from './types'
function value(input: unknown): string {
  if (typeof input === 'string') return JSON.stringify(input)
  if (Array.isArray(input)) return `[${input.map(value).join(', ')}]`
  if (input && typeof input === 'object')
    return `{ ${Object.entries(input)
      .map(([key, val]) => `${JSON.stringify(key)} = ${value(val)}`)
      .join(', ')} }`
  return String(input)
}
export function specToTOML(spec: Spec) {
  const lines: string[] = []
  const table = (input: Record<string, unknown>, path: string[]) => {
    if (path.length)
      lines.push(
        '',
        `[${path.map((part) => (/^[A-Za-z0-9_-]+$/.test(part) ? part : JSON.stringify(part))).join('.')}]`,
      )
    for (const [key, val] of Object.entries(input))
      if (val !== undefined && val !== null && (typeof val !== 'object' || Array.isArray(val)))
        lines.push(`${/^[A-Za-z0-9_-]+$/.test(key) ? key : JSON.stringify(key)} = ${value(val)}`)
    for (const [key, val] of Object.entries(input))
      if (val && typeof val === 'object' && !Array.isArray(val))
        table(val as Record<string, unknown>, [...path, key])
  }
  table(spec as unknown as Record<string, unknown>, [])
  return lines.join('\n').trim() + '\n'
}
export function downloadConfig(spec: Spec) {
  const url = URL.createObjectURL(new Blob([specToTOML(spec)], { type: 'application/toml' }))
  const anchor = document.createElement('a')
  anchor.href = url
  anchor.download = 'hakopod.toml'
  anchor.click()
  URL.revokeObjectURL(url)
}
