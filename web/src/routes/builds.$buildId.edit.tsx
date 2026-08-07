import { createFileRoute, useNavigate } from '@tanstack/react-router'
import { useQuery } from '@tanstack/react-query'
import BuildForm from '../components/build-form'
import { Empty, ErrorState, Loading } from '../components/shared'
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
      <Empty
        icon="lock"
        title="Deployment access required"
        description="Your project role does not allow build configuration changes."
      />
    )
  return (
    <BuildForm
      build={build.data}
      onClose={() => void navigate({ to: '/builds/$buildId', params: { buildId } })}
    />
  )
}
