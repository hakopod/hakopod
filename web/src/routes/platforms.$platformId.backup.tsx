import { createFileRoute } from '@tanstack/react-router'
import { PlatformRecoveryForm } from '../components/platform-recovery'

export const Route = createFileRoute('/platforms/$platformId/backup')({ component: Page })
function Page() {
  const { platformId } = Route.useParams()
  return <PlatformRecoveryForm key={platformId} id={platformId} kind="backup" />
}
