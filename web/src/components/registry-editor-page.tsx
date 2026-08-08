import { useNavigate } from '@tanstack/react-router'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { RegistryForm } from './registry-settings'
import { Empty, ErrorState, Loading } from './shared'
import { client, unwrap } from '../lib/client'
import { useScope } from '../lib/scope'
export default function RegistryEditorPage({ name }: { name?: string }) {
  const scope = useScope()
  const navigate = useNavigate()
  const cache = useQueryClient()
  const query = { project: scope.project, environment: scope.environment }
  const registries = useQuery({
    queryKey: ['registries', query.project, query.environment],
    queryFn: ({ signal }) => unwrap(client.GET('/registries', { signal, params: { query } })),
    enabled: Boolean(name),
    gcTime: 0,
  })
  const back = () => void navigate({ to: '/infrastructure', search: { tab: 'registries' } })
  if (!scope.can('deployments:write'))
    return (
      <Empty
        icon="lock"
        title="Deployment access required"
        description="Choose a project where you can configure registry credentials."
      />
    )
  if (name && registries.isPending) return <Loading />
  if (registries.error) return <ErrorState error={registries.error} />
  const registry = registries.data?.items.find((item) => item.name === name)
  if (name && !registry)
    return (
      <Empty
        title="Credential not found"
        description="This registry credential is absent from the selected environment."
      />
    )
  return (
    <RegistryForm
      key={name || 'new'}
      registry={registry}
      project={query.project}
      environment={query.environment}
      onClose={back}
      onSaved={() => {
        void cache.invalidateQueries({ queryKey: ['registries'] })
        back()
      }}
    />
  )
}
