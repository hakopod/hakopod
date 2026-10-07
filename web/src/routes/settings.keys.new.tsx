import { createFileRoute } from '@tanstack/react-router'
import { CreateKey } from './settings'
import { ErrorState, Loading, Note, PageHeader } from '../components/shared'
import { resolveProjectRouteScope, useProjects } from '../lib/projects'
export const Route = createFileRoute('/settings/keys/new')({
  validateSearch: (search: Record<string, unknown>) => ({
    project: typeof search.project === 'string' ? search.project : '',
    environment: typeof search.environment === 'string' ? search.environment : '',
  }),
  component: NewKey,
})
function NewKey() {
  const scope = Route.useSearch()
  const projects = useProjects()
  const selected = resolveProjectRouteScope(projects.data?.items, scope.project, scope.environment)
  if (
    !/^[a-z0-9][a-z0-9-]{0,62}$/.test(scope.project) ||
    !/^[a-z0-9][a-z0-9-]{0,62}$/.test(scope.environment)
  )
    return (
      <div>
        <PageHeader title="Create API key" />
        <Note>Choose a valid project and environment from API keys before creating a key.</Note>
      </div>
    )
  if (projects.isPending || (selected.status !== 'ready' && projects.isFetching)) return <Loading />
  if (projects.error) return <ErrorState error={projects.error} retry={() => void projects.refetch()} />
  if (selected.status !== 'ready') return (
    <div>
      <PageHeader title="Create API key" />
      <Note>This project or environment is unavailable. Choose an accessible scope from API keys before creating a key.</Note>
    </div>
  )
  return (
    <CreateKey
      page
      open
      onOpenChange={(open) => {
        if (!open) window.location.assign('/settings?tab=keys')
      }}
      project={scope.project}
      environment={scope.environment}
    />
  )
}
