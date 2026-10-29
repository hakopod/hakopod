import { createFileRoute } from '@tanstack/react-router'
import BackupRunForm from '../components/backup-run-form'

const text = (value: unknown) => (typeof value === 'string' && value ? value : undefined)

export const Route = createFileRoute('/backups/new')({
  // source is a backup source key and destination an ID; both only preselect choices.
  validateSearch: (search: Record<string, unknown>): { source?: string; destination?: string } => ({
    source: text(search.source),
    destination: text(search.destination),
  }),
  component: Page,
})
function Page() {
  const search = Route.useSearch()
  return <BackupRunForm initialSource={search.source} initialDestination={search.destination} />
}
