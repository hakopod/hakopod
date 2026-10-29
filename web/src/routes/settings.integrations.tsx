import { createFileRoute, Navigate, Outlet, useLocation } from '@tanstack/react-router'
import { Empty } from '../components/shared'
import { useScope } from '../lib/scope'
export const Route = createFileRoute('/settings/integrations')({ component: Integrations })
function Integrations() {
  const scope = useScope()
  const path = useLocation().pathname
  // The integrations list lives in the settings hub beside the other sections.
  if (path === '/settings/integrations')
    return <Navigate to="/settings" search={{ tab: 'integrations' }} replace />
  const slackChild = path === '/settings/integrations/slack'
  // Slack owns its own permission response so the protected product route
  // retains its branded heading and edition-specific access guidance.
  if (!scope.identity.admin && !slackChild)
    return (
      <Empty
        icon="lock"
        title="Administrator access required"
        description="Provider credentials are managed by installation administrators."
      />
    )
  return <Outlet />
}
