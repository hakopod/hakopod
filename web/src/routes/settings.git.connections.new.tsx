import { createFileRoute } from '@tanstack/react-router'
import { GitConnectionEditor } from '../components/git-connections'
import type { GitProvider } from '../lib/git-connections'
export const Route = createFileRoute('/settings/git/connections/new')({
  validateSearch: (search: Record<string, unknown>): { provider?: GitProvider } => ({
    provider: search.provider === 'gitlab' ? 'gitlab' : 'github',
  }),
  component: NewConnection,
})
function NewConnection() {
  const provider = Route.useSearch().provider === 'gitlab' ? 'gitlab' : 'github'
  return <GitConnectionEditor key={provider} initialProvider={provider} />
}
