import { useQuery } from '@tanstack/react-query'
import type { components } from './api.generated'
import { client, unwrap } from './client'
import { useScope } from './scope'

export type GitConnection = components['schemas']['GitConnection']
export type GitProvider = 'github' | 'gitlab'
export const gitNames = { github: 'GitHub', gitlab: 'GitLab' }
export const authNames = {
  token: 'Access token',
  github_app: 'GitHub App',
  gitlab_oauth: 'GitLab OAuth',
}
export function useGitConnections() {
  const scope = useScope()
  return useQuery({
    queryKey: ['git-connections'],
    queryFn: ({ signal }) => unwrap(client.GET('/git/connections', { signal })),
    enabled: scope.identity.admin || scope.identity.can_manage_git,
    staleTime: 15000,
    retry: false,
  })
}
export function gitConnectionOptions(
  items: GitConnection[],
  provider: GitProvider,
  current: string,
  builds: boolean,
) {
  const defaultID = `${provider}-default`
  const selected = current === defaultID ? '' : current
  const options = items
    .filter((item) => item.provider === provider)
    .map((item) => ({
      value: item.id === defaultID ? '' : item.id,
      label: `${item.name}${!item.enabled ? ' · Disabled' : builds && !item.capabilities.builds ? ' · Builds unavailable' : !builds && !item.capabilities.read_source ? ' · Source access unavailable' : ''}`,
      disabled:
        !item.enabled || (builds ? !item.capabilities.builds : !item.capabilities.read_source),
    }))
  if (!options.some((option) => option.value === ''))
    options.unshift({ value: '', label: 'Choose a connection', disabled: true })
  if (selected && !options.some((option) => option.value === selected))
    options.push({ value: current, label: `Unavailable connection · ${current}`, disabled: true })
  return options
}

export function useGitProviderSetup() {
  const scope = useScope()
  return useQuery({
    queryKey: ['git-provider-setup'],
    queryFn: ({ signal }) => unwrap(client.GET('/git/setup', { signal })),
    enabled: scope.identity.admin || scope.identity.can_manage_git,
    retry: false,
    staleTime: 15000,
  })
}
