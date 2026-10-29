import { createFileRoute } from '@tanstack/react-router'
import { useQuery } from '@tanstack/react-query'
import { client, unwrap } from '../lib/client'
import { useScope } from '../lib/scope'
import { Empty, ErrorState, Loading } from '../components/shared'
import { VirtualNetworkConnect } from '../components/virtual-network-connect'

type ConnectSearch = { segment?: string; application?: string; disconnect?: string }
const text = (value: unknown) => (typeof value === 'string' && value ? value : undefined)

export const Route = createFileRoute('/networks/$networkName/connect')({
  // segment/application preselect a grant; disconnect is "applicationId/service/localNetwork".
  validateSearch: (search: Record<string, unknown>): ConnectSearch => ({
    segment: text(search.segment),
    application: text(search.application),
    disconnect: text(search.disconnect),
  }),
  component: ConnectNetwork,
})
function ConnectNetwork() {
  const { networkName } = Route.useParams()
  const search = Route.useSearch()
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
        description="Your project role does not allow changing service connections. Ask for deployment access in this project."
      />
    )
  return (
    <VirtualNetworkConnect
      key={`${scope.project}:${scope.environment}:${networkName}:${search.disconnect || ''}`}
      project={scope.project}
      environment={scope.environment}
      network={network.data}
      initialSegment={search.segment}
      initialApplication={search.application}
      disconnect={search.disconnect}
    />
  )
}
