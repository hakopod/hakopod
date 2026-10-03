import { createFileRoute } from '@tanstack/react-router'
import { ErrorState, Loading } from '../components/shared'
import { canAccess, useResourceScope, useScope } from '../lib/scope'
import { useManagedPlatform, useManagedPlatformCatalog } from '../lib/managed-platforms'
import { PlatformForm, PlatformFormState } from './platforms.new'

export const Route = createFileRoute('/platforms/$platformId/configure')({ component: Page })
function Page() {
  const { platformId } = Route.useParams()
  return <Configure key={platformId} id={platformId} />
}
function Configure({ id }: { id: string }) {
  const query = useManagedPlatform(id)
  const { identity } = useScope()
  useResourceScope(query.data)
  const canManage = Boolean(
    query.data &&
    !identity.application &&
    canAccess(identity, query.data.project, 'deployments:write'),
  )
  const catalog = useManagedPlatformCatalog(
    query.data?.project || '',
    query.data?.environment || '',
    canManage,
  )
  if (query.isPending)
    return (
      <PlatformFormState editing>
        <Loading />
      </PlatformFormState>
    )
  if (!query.data)
    return (
      <PlatformFormState editing>
        <ErrorState error={query.error || new Error('Platform unavailable')} />
      </PlatformFormState>
    )
  if (!canManage)
    return (
      <PlatformFormState editing>
        <ErrorState error={new Error('You do not have permission to configure this platform.')} />
      </PlatformFormState>
    )
  if (catalog.isPending)
    return (
      <PlatformFormState editing>
        <Loading />
      </PlatformFormState>
    )
  if (catalog.error || !catalog.data)
    return (
      <PlatformFormState editing>
        <ErrorState error={catalog.error || new Error('Platform configuration unavailable')} />
      </PlatformFormState>
    )
  return (
    <PlatformForm
      initial={query.data}
      catalog={{
        ...catalog.data,
        items: catalog.data.items.filter((entry) => entry.kind === query.data.spec.kind),
      }}
    />
  )
}
