import { createFileRoute, useNavigate } from '@tanstack/react-router'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { SourceForm } from '../components/application-source'
import { Empty, ErrorState, Loading } from '../components/shared'
import { client, unwrap } from '../lib/client'
import { useScope, useResourceScope } from '../lib/scope'

export const Route = createFileRoute('/applications/$applicationId/source')({
  component: ConfigureSource,
})
function ConfigureSource() {
  const { applicationId } = Route.useParams()
  const navigate = useNavigate()
  const cache = useQueryClient()
  const scope = useScope()
  const application = useQuery({
    queryKey: ['application', applicationId],
    queryFn: ({ signal }) =>
      unwrap(client.GET('/applications/{id}', { signal, params: { path: { id: applicationId } } })),
    gcTime: 0,
  })
  const source = useQuery({
    queryKey: ['application-source', applicationId],
    queryFn: ({ signal }) =>
      unwrap(
        client.GET('/applications/{id}/source', {
          signal,
          params: { path: { id: applicationId } },
        }),
      ),
    gcTime: 0,
  })
  const back = () =>
    void navigate({
      to: '/applications/$applicationId',
      params: { applicationId },
      search: { tab: 'source' },
    })
  useResourceScope(application.data)
  if (application.isPending || source.isPending) return <Loading />
  if (application.error || source.error || !application.data)
    return <ErrorState error={application.error || source.error} />
  if (!scope.can('deployments:write'))
    return (
      <Empty
        icon="lock"
        title="Deployment access required"
        description="Your project role does not allow repository configuration changes."
      />
    )
  return (
    <SourceForm
      application={application.data}
      source={source.data?.source}
      onClose={back}
      onSaved={() => {
        void cache.invalidateQueries({ queryKey: ['application-source', applicationId] })
        back()
      }}
    />
  )
}
