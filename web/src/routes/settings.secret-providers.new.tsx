import { createFileRoute } from '@tanstack/react-router'
import { SecretProviderEditor } from '../components/secret-provider-editor'
export const Route = createFileRoute('/settings/secret-providers/new')({
  component: () => <SecretProviderEditor />,
})
