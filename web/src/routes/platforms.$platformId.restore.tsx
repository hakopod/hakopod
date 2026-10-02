import { createFileRoute } from '@tanstack/react-router'
import { PlatformRecoveryForm } from '../components/platform-recovery'

export const Route = createFileRoute('/platforms/$platformId/restore')({ component: Page })
function Page() {
  const { platformId } = Route.useParams()
  return <PlatformRecoveryForm key={platformId} id={platformId} kind="restore" />
}
