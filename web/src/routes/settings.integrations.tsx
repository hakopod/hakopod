import { createFileRoute, Outlet, useLocation } from '@tanstack/react-router'
import { Empty, PageHeader } from '../components/shared'
import { SlackSettingsPanel } from '../components/slack-settings'
import { useScope } from '../lib/scope'
export const Route = createFileRoute('/settings/integrations')({ component: Integrations })
function Integrations() {
  const scope = useScope()
  const path = useLocation().pathname
  if (path === '/settings/integrations')
    return (
      <div className="grid gap-4">
        <PageHeader title="Integrations" />
        <SlackSettingsPanel />
      </div>
    )
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
