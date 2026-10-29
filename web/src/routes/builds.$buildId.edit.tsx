import { createFileRoute, useNavigate } from '@tanstack/react-router'
import { useQuery } from '@tanstack/react-query'
import BuildForm from '../components/build-form'
import { ErrorState, Loading, PageHeader } from '../components/shared'
import { ResourceEmpty } from '../components/resource-card'
import { client, unwrap } from '../lib/client'
import { useScope, useResourceScope } from '../lib/scope'

export const Route = createFileRoute('/builds/$buildId/edit')({ component: EditBuild })
function EditBuild() {
  const { buildId } = Route.useParams()
  const navigate = useNavigate()
  const scope = useScope()
  const build = useQuery({
    queryKey: ['build', buildId],
    queryFn: ({ signal }) =>
      unwrap(client.GET('/builds/{id}', { signal, params: { path: { id: buildId } } })),
    gcTime: 0,
  })
  useResourceScope(build.data)
  if (build.isPending) return <Loading />
  if (build.error || !build.data) return <ErrorState error={build.error} />
  if (!scope.can('deployments:write'))
    return (
      <div className="ops-page builds-page">
        <PageHeader title="Edit source build" />
        <ResourceEmpty
          icon="lock"
          title="Deployment access required"
          description="Your role in this project does not allow build configuration changes. Ask a project administrator for access."
        />
      </div>
    )
  return (
    <BuildForm
      build={build.data}
      onClose={() => void navigate({ to: '/builds/$buildId', params: { buildId } })}
    />
  )
}
