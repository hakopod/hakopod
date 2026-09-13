import { useQuery } from '@tanstack/react-query'
import { client, unwrap } from '../lib/client'
import { useProjects } from '../lib/projects'
import { useScope } from '../lib/scope'
import { SecretProviderForm } from './secret-provider-form'
import { Empty, ErrorState, Loading } from './shared'

export function SecretProviderEditor({ name }: { name?: string }) {
  const scope = useScope()
  const projects = useProjects()
  const provider = useQuery({
    queryKey: ['secret-provider', name],
    queryFn: ({ signal }) =>
      unwrap(
        client.GET('/secret-providers/{name}', { signal, params: { path: { name: name || '' } } }),
      ),
    enabled: Boolean(name) && scope.identity.admin,
    gcTime: 0,
    refetchOnWindowFocus: false,
  })
  if (!scope.identity.admin)
    return (
      <Empty
        icon="lock"
        title="Administrator access required"
        description="Secret providers are configured by installation administrators."
      />
    )
  if (projects.isPending || (name && provider.isPending)) return <Loading />
  if (projects.error)
    return <ErrorState error={projects.error} retry={() => void projects.refetch()} />
  if (provider.error)
    return <ErrorState error={provider.error} retry={() => void provider.refetch()} />
  return (
    <SecretProviderForm
      key={name || 'new'}
      provider={provider.data}
      projects={projects.data?.items || []}
    />
  )
}
