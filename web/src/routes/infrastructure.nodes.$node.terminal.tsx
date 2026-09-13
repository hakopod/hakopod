import { createFileRoute } from '@tanstack/react-router'
import { useQuery } from '@tanstack/react-query'
import { client, unwrap } from '../lib/client'
import PodTerminal from '../components/pod-terminal'
import { FormPage } from '../components/form-page'
import { Empty, ErrorState, Loading } from '../components/shared'
export const Route = createFileRoute('/infrastructure/nodes/$node/terminal')({
  component: HostTerminal,
})
function HostTerminal() {
  const { node } = Route.useParams()
  const access = useQuery({
    queryKey: ['host-access'],
    queryFn: ({ signal }) => unwrap(client.GET('/host-access', { signal })),
    gcTime: 0,
  })
  if (access.isPending) return <Loading />
  if (access.error) return <ErrorState error={access.error} />
  const target = access.data?.nodes.find((item) => item.name === node)
  if (!target?.allowed)
    return (
      <Empty
        icon="lock"
        title="Host terminal access required"
        description="The super admin must grant explicit authority for this node."
      />
    )
  return (
    <FormPage
      title={node}
      description={`${target.control_plane ? 'Control-plane' : 'Worker'} Linux node · Root shell with a ten-minute limit and two-minute idle timeout.`}
      breadcrumbs={[
        { label: 'Infrastructure', to: '/infrastructure' },
        { label: node },
        { label: 'Host terminal' },
      ]}
      icon="terminal"
    >
      <PodTerminal key={node} hostNode={node} />
    </FormPage>
  )
}
