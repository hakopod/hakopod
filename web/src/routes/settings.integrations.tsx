import { createFileRoute, Navigate, Outlet, useLocation } from '@tanstack/react-router'
import { Empty } from '../components/shared'
import { useScope } from '../lib/scope'
export const Route = createFileRoute('/settings/integrations')({ component: Integrations })
function Integrations() {
  const scope = useScope()
  const path = useLocation().pathname
  if (!scope.identity.admin)
    return (
      <Empty
        icon="lock"
        title="Administrator access required"
        description="Provider credentials are managed by installation administrators."
      />
    )
  if (path !== '/settings/integrations') return <Outlet />
  return <Navigate to="/settings" search={{ tab: 'github' }} replace />
}
