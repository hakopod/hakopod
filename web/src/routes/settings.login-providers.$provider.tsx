import { createFileRoute, notFound } from '@tanstack/react-router'
import { LoginProviderEditor } from '../components/login-provider-settings'
import { loginProviders, type LoginProvider } from '../lib/installation-settings'
export const Route = createFileRoute('/settings/login-providers/$provider')({
  beforeLoad: ({ params }) => {
    if (!loginProviders.includes(params.provider as LoginProvider)) throw notFound()
  },
  component: () => <LoginProviderEditor provider={Route.useParams().provider as LoginProvider} />,
})
