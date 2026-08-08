import { createFileRoute } from '@tanstack/react-router'
import { GitConnectionSettings } from '../components/application-source'
import { FormPage, FormHint } from '../components/form-page'
import { Empty } from '../components/shared'
import { ServiceIcon } from '../components/service-icon'
export const Route = createFileRoute('/settings/integrations/$provider')({
  component: ProviderIntegration,
})
function ProviderIntegration() {
  const { provider } = Route.useParams()
  if (provider !== 'github' && provider !== 'gitlab')
    return (
      <Empty
        title="Provider not found"
        description="Choose GitHub or GitLab from the integrations page."
      />
    )
  const name = provider === 'github' ? 'GitHub' : 'GitLab'
  return (
    <FormPage
      title={`Connect ${name}`}
      description="Credentials stay on the server. Webhook events are authenticated before use."
      breadcrumbs={[
        { label: 'Account & access', to: '/settings' },
        { label: 'Integrations', to: '/settings/integrations' },
        { label: name },
      ]}
      icon="branch"
      help={
        <>
          <div className="template-identity">
            <ServiceIcon name={provider} size={40} />
            <h2>{name}</h2>
          </div>
          <FormHint title="Use a dedicated integration token">
            Give the token access to the repositories you intend to deploy. Workflow installation
            also needs repository write access.
          </FormHint>
        </>
      }
    >
      <GitConnectionSettings provider={provider} />
    </FormPage>
  )
}
