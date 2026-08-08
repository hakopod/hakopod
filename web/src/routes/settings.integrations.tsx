import { createFileRoute, Outlet, useLocation } from '@tanstack/react-router'
import { GitHubSettings } from '../components/application-source'
import { FormPage } from '../components/form-page'
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
  return (
    <FormPage
      title="Git integrations"
      description="Connect the provider accounts used by repository sources and builds."
      breadcrumbs={[{ label: 'Account & access', to: '/settings' }, { label: 'Integrations' }]}
      icon="branch"
    >
      <GitHubSettings />
    </FormPage>
  )
}
