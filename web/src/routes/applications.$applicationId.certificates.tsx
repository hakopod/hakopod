import { createFileRoute } from '@tanstack/react-router'
import { useQuery } from '@tanstack/react-query'
import { client, unwrap } from '../lib/client'
import { useResourceScope, useScope } from '../lib/scope'
import { Empty, ErrorState, Loading } from '../components/shared'
import { BackendCertificateForm } from '../components/backend-certificate-form'

export const Route = createFileRoute('/applications/$applicationId/certificates')({
  validateSearch: (search: Record<string, unknown>): { service?: string } => ({
    service: typeof search.service === 'string' ? search.service : undefined,
  }),
  component: Certificates,
})
function Certificates() {
  const { applicationId } = Route.useParams()
  const { service } = Route.useSearch()
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
        title="Deployment access required"
        description="Your project role does not allow certificate changes."
      />
    )
  if (!service || !application.data.spec.services[service])
    return (
      <Empty
        title="Service not found"
        description="Open a service to manage its backend certificates."
      />
    )
  return (
    <BackendCertificateForm
      key={`${applicationId}:${service}`}
      application={application.data}
      serviceName={service}
    />
  )
}
