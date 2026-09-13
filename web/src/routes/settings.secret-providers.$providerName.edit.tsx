import { createFileRoute } from '@tanstack/react-router'
import { SecretProviderEditor } from '../components/secret-provider-editor'
export const Route = createFileRoute('/settings/secret-providers/$providerName/edit')({
  component: EditProvider,
})
function EditProvider() {
  const { providerName } = Route.useParams()
  return <SecretProviderEditor name={providerName} />
}
