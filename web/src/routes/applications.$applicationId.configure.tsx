import { createFileRoute, useNavigate } from '@tanstack/react-router'
import { useQuery } from '@tanstack/react-query'
import { DeploymentForm } from '../components/deploy-dialog'
import { Empty, ErrorState, Loading } from '../components/shared'
import { client, unwrap } from '../lib/client'
import { useScope, useResourceScope } from '../lib/scope'

export const Route = createFileRoute('/applications/$applicationId/configure')({
  validateSearch: (
    search: Record<string, unknown>,
  ): { mode?: 'form' | 'toml'; service?: string } => ({
    mode: search.mode === 'toml' ? 'toml' : 'form',
    service: typeof search.service === 'string' ? search.service : undefined,
  }),
  component: ConfigureApplication,
})
function ConfigureApplication() {
  const { applicationId } = Route.useParams()
  const { mode, service } = Route.useSearch()
  const navigate = useNavigate()
  const scope = useScope()
  const application = useQuery({
    queryKey: ['application', applicationId],
    queryFn: ({ signal }) =>
      unwrap(client.GET('/applications/{id}', { signal, params: { path: { id: applicationId } } })),
    gcTime: 0,
  })
  useResourceScope(application.data)
  if (application.isPending) return <Loading />
  if (application.error || !application.data) return <ErrorState error={application.error} />
  if (!scope.can('deployments:write'))
    return (
      <Empty
        icon="lock"
        title="Deployment access required"
        description="Your project role does not allow configuration changes."
      />
    )
  if (service && !application.data.spec.services[service])
    return (
      <Empty
        title="Service not found"
        description="This service is absent from the current application revision."
      />
    )
  return (
    <DeploymentForm
      application={application.data}
      initialMode={mode}
      serviceName={service}
      onClose={() =>
        void navigate({
          to: '/applications/$applicationId',
          params: { applicationId },
          search: { service },
        })
      }
    />
  )
}
