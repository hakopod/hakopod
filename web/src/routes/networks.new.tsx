import { createFileRoute } from '@tanstack/react-router'
import { useQuery } from '@tanstack/react-query'
import { client, unwrap } from '../lib/client'
import { useScope } from '../lib/scope'
import { Empty, ErrorState, Loading } from '../components/shared'
import { VirtualNetworkForm } from '../components/virtual-network-form'
import { dashboardEdition, useEditionFeatures } from '../lib/dashboard-edition'
import { Button } from '../components/ui/button'

export const Route = createFileRoute('/networks/new')({ component: NewNetwork })
function NewNetwork() {
  const scope = useScope()
  const features = useEditionFeatures()
  const permission = useQuery({
    queryKey: ['virtual-networks', scope.project, scope.environment],
    queryFn: ({ signal }) =>
      unwrap(
        client.GET('/virtual-networks', {
          signal,
          params: { query: { project: scope.project, environment: scope.environment } },
        }),
      ),
    enabled: Boolean(scope.project && scope.environment && !features.hostedCompute),
    gcTime: 0,
  })
  if (features.hostedCompute)
    return (
      <Empty
        title="Shared networks need your own server"
        description="Hosted Free includes private service networking. Connecting multiple applications through a shared network requires your own server."
        action={
          <Button asChild>
            <a href={features.computeURL}>Choose compute</a>
          </Button>
        }
      />
    )
  if (permission.isPending) return <Loading />
  if (permission.error)
    return <ErrorState error={permission.error} retry={() => void permission.refetch()} />
  if (!permission.data?.can_manage)
    return (
      <Empty
        icon="lock"
        title={
          dashboardEdition.cloud
            ? 'Workspace owner access required'
            : 'Project administrator access required'
        }
        description={
          dashboardEdition.cloud
            ? 'Your workspace owner can manage private networks. Update the connected node and reconnect it with a project/environment key granting networks:write, deployments:read and deployments:write.'
            : 'Project administrators manage virtual networks and application grants.'
        }
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
