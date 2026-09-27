import { createFileRoute } from '@tanstack/react-router'
import { useDatabase } from '../lib/databases'
import { DatabaseForm } from '../components/database-form'
import { ErrorState, Loading } from '../components/shared'
export const Route = createFileRoute('/databases/$databaseId/resize')({ component: Page })
function Page() {
  const query = useDatabase(Route.useParams().databaseId)
  if (query.isPending) return <Loading />
  if (query.error) return <ErrorState error={query.error} />
  return (
    <DatabaseForm
      key={query.data.id}
      database={query.data}
      project={query.data.project}
      environment={query.data.environment}
    />
  )
}
