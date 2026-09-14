import type { ReactNode } from 'react'
import { useInstallationAccess } from '../lib/installation-settings'
import { Empty, ErrorState, Loading } from './shared'

export function InstallationAccess({ children }: { children: ReactNode }) {
  const access = useInstallationAccess()
  if (!access.admin)
    return (
      <Empty
        title="Administrator access required"
        description="An installation administrator manages these settings."
      />
    )
  if (access.status.isPending) return <Loading />
  if (access.status.error)
    return <ErrorState error={access.status.error} retry={() => void access.status.refetch()} />
  if (!access.allowed)
    return (
      <Empty
        title="Managed by Hakopod Cloud"
        description="Installation email and sign-in settings are managed by the Cloud service."
      />
    )
  return children
}
