import { createFileRoute, useNavigate } from '@tanstack/react-router'
import { useQuery } from '@tanstack/react-query'
import BuildForm from '../components/build-form'
import { Empty, ErrorState, Loading } from '../components/shared'
import { client, unwrap } from '../lib/client'
import { useScope, useResourceScope } from '../lib/scope'

export const Route = createFileRoute('/builds/new')({
  validateSearch: (search: Record<string, unknown>): { application?: string } => ({
    application: typeof search.application === 'string' ? search.application : undefined,
  }),
  component: NewBuild,
})
function NewBuild() {
  const { application: applicationId } = Route.useSearch()
  const navigate = useNavigate()
  const scope = useScope()
  const application = useQuery({
    queryKey: ['application', applicationId],
    queryFn: ({ signal }) =>
      unwrap(
        client.GET('/applications/{id}', { signal, params: { path: { id: applicationId! } } }),
      ),
    enabled: Boolean(applicationId),
    gcTime: 0,
  })
  useResourceScope(application.data)
  if (!scope.can('deployments:write'))
    return (
      <Empty
        icon="lock"
        title="Deployment access required"
        description="Choose a project where you can configure builds."
      />
    )
  if (applicationId && application.isPending) return <Loading />
  if (application.error) return <ErrorState error={application.error} />
  return (
    <BuildForm application={application.data} onClose={() => void navigate({ to: '/builds' })} />
  )
}
