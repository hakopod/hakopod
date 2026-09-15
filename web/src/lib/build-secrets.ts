export function parseBuildSecrets(value: string): Record<string, string> {
  const result: Record<string, string> = {}
  for (const line of value.split('\n')) {
    if (!line.trim()) continue
    const match = /^([a-z][a-z0-9_-]{0,63})=([A-Z][A-Z0-9_]{0,99})$/.exec(line.trim())
    if (!match || Object.hasOwn(result, match[1]))
      throw new Error(
        'Use one unique mount_id=CI_SECRET_NAME per line. Enter secret names, not values.',
      )
    result[match[1]] = match[2]
  }
  if (Object.keys(result).length > 16) throw new Error('At most 16 build secrets are allowed.')
  return result
}
export function formatBuildSecrets(value?: Record<string, string>): string {
  return Object.entries(value || {})
    .map(([id, ref]) => id + '=' + ref)
    .join('\n')
}
