import { createFileRoute, Link } from '@tanstack/react-router'
import { Note } from '../components/shared'
import { FormPage } from '../components/form-page'
import { Button } from '../components/ui/button'
import { databaseSearch } from '../lib/databases'

export const Route = createFileRoute('/databases/external/new')({ validateSearch: databaseSearch, component: Page })
function Page() {
  const scope = Route.useSearch()
  return <FormPage title="External database unavailable" description="New external database connections cannot be created." breadcrumbs={[]}><div className="grid gap-4"><Note>Existing records remain available for inspection, credential rotation, disconnection and removal.</Note><Button asChild className="w-fit"><Link to="/databases" search={scope}>Back to databases</Link></Button></div></FormPage>
}
