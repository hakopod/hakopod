import { createFileRoute } from '@tanstack/react-router'
import BackupDestinationPage from '../components/backup-destination-form'
export const Route = createFileRoute('/backups/destinations/new')({ component: Page })
function Page() {
  return <BackupDestinationPage />
}
