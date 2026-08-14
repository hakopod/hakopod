import { createFileRoute } from '@tanstack/react-router'
import BackupSchedulePage from '../components/backup-schedule-form'
export const Route = createFileRoute('/backups/schedules/$scheduleId/edit')({ component: Page })
function Page() {
  return <BackupSchedulePage id={Route.useParams().scheduleId} />
}
