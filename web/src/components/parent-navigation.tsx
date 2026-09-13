import { Link, useLocation } from '@tanstack/react-router'
import { useQuery } from '@tanstack/react-query'
import { client, unwrap } from '../lib/client'
import { parentNavigation, type ParentNavigation } from '../lib/navigation'
import { useScope } from '../lib/scope'
import { Button } from './ui/button'
import { Tooltip } from './ui/surfaces'
import { Icon } from './icons'

function ParentLink({ parent }: { parent: ParentNavigation | null }) {
  if (!parent) return null
  return (
    <Tooltip content={parent.label}>
      <Button variant="ghost" size="icon" className="hako-parent-back" asChild>
        <Link to={parent.to} search={parent.search} aria-label={parent.label}>
          <Icon name="back" size={18} />
        </Link>
      </Button>
    </Tooltip>
  )
}

function DeploymentParent({ id }: { id: string }) {
  const deployment = useQuery({
    queryKey: ['deployment', id],
    queryFn: ({ signal }) =>
      unwrap(client.GET('/deployments/{id}', { signal, params: { path: { id } } })),
    select: (release) => release.application_id,
    staleTime: 5000,
    gcTime: 0,
  })
  return <ParentLink parent={parentNavigation(`/deployments/${id}`, {}, deployment.data)} />
}

export function ParentBackLink() {
  const location = useLocation()
  const scope = useScope()
  const deployment = /^\/deployments\/([^/]+)$/.exec(location.pathname)
  return deployment ? (
    <DeploymentParent id={deployment[1]} />
  ) : (
    <ParentLink parent={parentNavigation(location.pathname, location.search, undefined, scope)} />
  )
}
