import { createFileRoute } from '@tanstack/react-router'
import { useQuery } from '@tanstack/react-query'
import { client, unwrap } from '../lib/client'
import { useScope } from '../lib/scope'
import { Empty, ErrorState, Loading } from '../components/shared'
import { VirtualNetworkForm } from '../components/virtual-network-form'

export const Route = createFileRoute('/networks/new')({ component: NewNetwork })
function NewNetwork() {
  const scope = useScope()
  const permission = useQuery({
    queryKey: ['virtual-networks', scope.project, scope.environment],
    queryFn: ({ signal }) =>
      unwrap(
        client.GET('/virtual-networks', {
          signal,
          params: { query: { project: scope.project, environment: scope.environment } },
        }),
      ),
    enabled: Boolean(scope.project && scope.environment),
    gcTime: 0,
  })
  if (permission.isPending) return <Loading />
  if (permission.error)
    return <ErrorState error={permission.error} retry={() => void permission.refetch()} />
  if (!permission.data?.can_manage)
    return (
      <Empty
        icon="lock"
        title="Project administrator access required"
        description="Project administrators manage virtual networks and application grants."
      />
    )
  return (
    <VirtualNetworkForm
      key={`${scope.project}:${scope.environment}`}
      project={scope.project}
      environment={scope.environment}
    />
  )
}
