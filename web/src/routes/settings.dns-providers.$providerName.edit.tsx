import { createFileRoute } from '@tanstack/react-router'
import { DNSProviderEditor } from '../components/dns-provider-credential-fields'
export const Route = createFileRoute('/settings/dns-providers/$providerName/edit')({
  // A credential name is unique per scope, so the scope travels with the link.
  validateSearch: (
    search: Record<string, unknown>,
  ): { project?: string; environment?: string } => ({
    project: typeof search.project === 'string' ? search.project : undefined,
    environment: typeof search.environment === 'string' ? search.environment : undefined,
  }),
  component: EditDNSProvider,
})
function EditDNSProvider() {
  const { providerName } = Route.useParams()
  const { project, environment } = Route.useSearch()
  return (
    <DNSProviderEditor
      name={providerName}
      project={project || ''}
      environment={environment || ''}
    />
  )
}
