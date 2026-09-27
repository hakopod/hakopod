import { createFileRoute } from '@tanstack/react-router'
import { DatabaseForm } from '../components/database-form'
import { Empty } from '../components/shared'
import { databaseSearch } from '../lib/databases'
export const Route = createFileRoute('/databases/new')({
  validateSearch: databaseSearch,
  component: Page,
})
function Page() {
  const scope = Route.useSearch()
  return scope.project && scope.environment ? (
    <DatabaseForm key={`${scope.project}/${scope.environment}`} {...scope} />
  ) : (
    <Empty
      title="Choose a project and environment"
      description="Open Databases from the project where this database belongs."
    />
  )
}
