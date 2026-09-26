import { createFileRoute } from '@tanstack/react-router'
import { DNSProviderEditor } from '../components/dns-provider-credential-fields'
export const Route = createFileRoute('/settings/dns-providers/new')({
  component: () => <DNSProviderEditor />,
})
