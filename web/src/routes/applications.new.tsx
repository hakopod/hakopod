import { createFileRoute, useNavigate, Navigate } from '@tanstack/react-router'
import { DeploymentForm } from '../components/deploy-dialog'
import { Empty } from '../components/shared'
import { useScope } from '../lib/scope'

export const Route = createFileRoute('/applications/new')({
  validateSearch: (search: Record<string, unknown>): { mode?: 'form' | 'toml' | 'repository' } => ({
    mode: ['form', 'toml', 'repository'].includes(String(search.mode))
      ? (search.mode as 'form' | 'toml' | 'repository')
      : undefined,
  }),
  component: NewApplication,
})
function NewApplication() {
  const navigate = useNavigate()
  const scope = useScope()
  const { mode } = Route.useSearch()
  if (!scope.can('deployments:write'))
    return (
      <Empty
        icon="lock"
        title="Deployment access required"
        description="Choose a project where you can create applications."
      />
    )
  if (mode === 'repository') return <Navigate to="/applications/import" />
  return (
    <DeploymentForm
      initialMode={mode === 'toml' ? 'toml' : 'form'}
      onClose={() =>
        scope.project
          ? void navigate({
              to: '/projects/$project',
              params: { project: scope.project },
              search: { environment: scope.environment || undefined },
            })
          : void navigate({ to: '/' })
      }
    />
  )
}
