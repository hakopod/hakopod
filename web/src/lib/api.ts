export class APIError extends Error {
  constructor(
    message: string,
    public status: number,
    public code?: string,
  ) {
    super(message)
    this.name = 'APIError'
  }
}

export function message(error: unknown) {
  return error instanceof Error ? error.message : 'An unexpected error occurred.'
}
export const activeDeployment = (status?: string) => status === 'queued' || status === 'running'
export function timestamp(date?: string | null) {
  return date
    ? new Date(date).toLocaleString(undefined, {
        month: 'short',
        day: 'numeric',
        hour: '2-digit',
        minute: '2-digit',
      })
    : '—'
}
export function relative(date?: string | null) {
  if (!date) return 'Not yet'
  const elapsed = Math.max(0, Date.now() - new Date(date).getTime())
  if (elapsed < 60000) return 'Just now'
  if (elapsed < 3600000) return `${Math.floor(elapsed / 60000)}m ago`
  if (elapsed < 86400000) return `${Math.floor(elapsed / 3600000)}h ago`
  return `${Math.floor(elapsed / 86400000)}d ago`
}
