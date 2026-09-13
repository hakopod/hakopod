import { createFileRoute } from '@tanstack/react-router'
import { useQuery } from '@tanstack/react-query'
import { client, unwrap } from '../lib/client'
import { useScope } from '../lib/scope'
import { Empty, ErrorState, Loading } from '../components/shared'
import { VirtualNetworkConnect } from '../components/virtual-network-connect'

export const Route = createFileRoute('/networks/$networkName/connect')({
  component: ConnectNetwork,
})
function ConnectNetwork() {
  const { networkName } = Route.useParams()
  const scope = useScope()
  const network = useQuery({
    queryKey: ['virtual-network', scope.project, scope.environment, networkName],
    queryFn: ({ signal }) =>
      unwrap(
        client.GET('/virtual-networks/{name}', {
          signal,
          params: {
            path: { name: networkName },
            query: { project: scope.project, environment: scope.environment },
          },
        }),
      ),
    enabled: Boolean(scope.project && scope.environment),
    gcTime: 0,
    refetchOnWindowFocus: false,
  })
  if (network.isPending) return <Loading />
  if (network.error || !network.data)
    return <ErrorState error={network.error} retry={() => void network.refetch()} />
  if (!scope.can('deployments:write'))
    return (
      <Empty
        icon="lock"
        title="Deployment access required"
        description="Your project role does not allow service connections."
      />
    )
  return (
    <VirtualNetworkConnect
      key={`${scope.project}:${scope.environment}:${networkName}`}
      project={scope.project}
      environment={scope.environment}
      network={network.data}
    />
  )
}
