import type { ReactNode } from 'react'
import { useInstallationAccess } from '../lib/installation-settings'
import { Empty, ErrorState, Loading } from './shared'

export function InstallationAccess({
  children,
  managedDescription = 'Installation email and sign-in settings are managed by the Cloud service.',
  renderDenied,
}: {
  children: ReactNode
  managedDescription?: string
  renderDenied?: (state: { title: string; description: string }) => ReactNode
}) {
  const access = useInstallationAccess()
  const denied = (title: string, description: string) =>
    renderDenied ? (
      renderDenied({ title, description })
    ) : (
      <Empty title={title} description={description} />
    )
  if (!access.admin)
    return denied(
      'Administrator access required',
      'An installation administrator manages these settings.',
    )
  if (access.status.isPending) return <Loading />
  if (access.status.error)
    return <ErrorState error={access.status.error} retry={() => void access.status.refetch()} />
  if (!access.allowed) return denied('Managed by Hakopod Cloud', managedDescription)
  return children
}
