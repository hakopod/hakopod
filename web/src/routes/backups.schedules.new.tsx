import { createFileRoute } from '@tanstack/react-router'
import BackupSchedulePage from '../components/backup-schedule-form'
export const Route = createFileRoute('/backups/schedules/new')({ component: Page })
function Page() {
  return <BackupSchedulePage />
}
