import { createFileRoute } from '@tanstack/react-router'
import BackupRunForm from '../components/backup-run-form'
export const Route = createFileRoute('/backups/new')({ component: Page })
function Page() {
  return <BackupRunForm />
}
