import { useQuery } from '@tanstack/react-query'
import { useEditionWorkspace } from '../lib/dashboard-edition'
import { client, unwrap } from '../lib/client'
import { Button } from './ui/button'

export function DatabaseExplorerLink({
  project,
  environment,
}: {
  project: string
  environment: string
}) {
  const workspace = useEditionWorkspace().id || ''
  const query = useQuery({
    queryKey: ['database-explorer', workspace, project, environment],
    queryFn: ({ signal }) =>
      unwrap(
        client.GET('/database-explorer/connections', {
          signal,
          params: { query: { project, environment } },
        }),
      ),
    enabled: Boolean(project && environment),
    refetchInterval: 30000,
    gcTime: 0,
  })
  if (!query.data?.available) return null
  const context = btoa(JSON.stringify({ project, environment, workspace }))
    .replaceAll('+', '-')
    .replaceAll('/', '_')
    .replace(/=+$/, '')
  return (
    <Button asChild>
      <a href={`/synehq/s/${context}/connections/`} target="_blank" rel="noopener noreferrer">
        Explore databases
      </a>
    </Button>
  )
}
