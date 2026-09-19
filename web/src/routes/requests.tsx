import { createFileRoute } from '@tanstack/react-router'
import { PageHeader } from '../components/shared'
import { Requests, requestSearch } from '../components/requests'
export const Route = createFileRoute('/requests')({
  validateSearch: requestSearch,
  component: RequestsPage,
})
function RequestsPage() {
  const search = Route.useSearch()
  return (
    <div className="ops-page">
      <PageHeader
        title="Requests"
        description="HTTP ingress traffic across your accessible applications. Inspect response failures, latency and selected backends."
      />
      <Requests key={JSON.stringify(search)} initial={search} />
    </div>
  )
}
