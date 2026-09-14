import { createFileRoute, Navigate } from '@tanstack/react-router'
import { Empty } from '../components/shared'
export const Route = createFileRoute('/settings/integrations/$provider')({
  component: ProviderIntegration,
})
function ProviderIntegration() {
  const { provider } = Route.useParams()
  if (provider !== 'github' && provider !== 'gitlab')
    return (
      <Empty
        title="Provider not found"
        description="Choose GitHub or GitLab from the integrations page."
      />
    )
  return <Navigate to="/settings/git/connections/new" search={{ provider }} replace />
}
