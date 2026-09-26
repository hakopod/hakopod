import { createFileRoute, useNavigate, Navigate } from '@tanstack/react-router'
import { DeploymentForm } from '../components/deploy-dialog'
import { Empty, Loading } from '../components/shared'
import { useEditionFeatures } from '../lib/dashboard-edition'
import { useGitConnections } from '../lib/git-connections'
import { useScope } from '../lib/scope'

export const Route = createFileRoute('/applications/new')({
  validateSearch: (
    search: Record<string, unknown>,
  ): { mode?: 'form' | 'toml' | 'compose' | 'repository' } => ({
    mode: ['form', 'toml', 'compose', 'repository'].includes(String(search.mode))
      ? (search.mode as 'form' | 'toml' | 'compose' | 'repository')
      : undefined,
  }),
  component: NewApplication,
})
function NewApplication() {
  const navigate = useNavigate()
  const scope = useScope()
  const features = useEditionFeatures()
  const { mode } = Route.useSearch()
  // Matches the enabled condition of the connections query below; without it a
  // disabled query stays pending and would hold the page on the loading state.
  const canManageGit = features.git && (scope.identity.admin || scope.identity.can_manage_git)
  const connections = useGitConnections()
  if (!scope.can('deployments:write'))
    return (
      <Empty
        icon="lock"
        title="Deployment access required"
        description="Choose a project where you can create applications."
      />
    )
  if (mode === 'repository') return <Navigate to="/applications/import" />
  // Building from source is the default path, but only for someone who can
  // finish one: it needs a Git connection that is allowed to run builds. Anyone
  // else, and any explicit mode, keeps the container image and file forms.
  if (!mode && canManageGit) {
    if (connections.isPending) return <Loading />
    if (connections.data?.items.some((item) => item.enabled && item.capabilities.builds))
      return <Navigate to="/builds/new" />
  }
  return (
    <DeploymentForm
      initialMode={mode === 'toml' || mode === 'compose' ? mode : 'form'}
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
