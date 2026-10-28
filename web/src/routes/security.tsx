import { createFileRoute } from '@tanstack/react-router'
import { APIError } from '../lib/api'
import { useSecurityOverview, useSecuritySettings } from '../lib/security'
import { disclaimer, SecurityReview } from '../components/security-review'
import { ErrorState, Loading, PageHeader } from '../components/shared'

export const Route = createFileRoute('/security')({
  validateSearch: (search: Record<string, unknown>): { application?: string } => ({
    application: typeof search.application === 'string' ? search.application : undefined,
  }),
  component: Security,
})

function Security() {
  const search = Route.useSearch()
  const overview = useSecurityOverview()
  const settings = useSecuritySettings()
  return (
    <div className="ops-page security-page">
      <PageHeader
        title="Security"
        description={`Hakopod checks running applications against the signed Hakopod advisory feed and OSV.dev, using runtime versions from image configuration. ${disclaimer}`}
      />
      {overview.isPending ? (
        <Loading />
      ) : overview.error ? (
        <ErrorState
          title={
            overview.error instanceof APIError && overview.error.status === 403
              ? 'Access denied'
              : undefined
          }
          error={overview.error}
          retry={() => void overview.refetch()}
        />
      ) : (
        <SecurityReview
          overview={overview.data}
          settings={settings.data}
          expanded={search.application}
        />
      )}
    </div>
  )
}
