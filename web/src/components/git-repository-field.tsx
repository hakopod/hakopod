import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { client, unwrap } from '../lib/client'
import { message } from '../lib/api'
import { useGitConnections, type GitProvider } from '../lib/git-connections'
import { useScope } from '../lib/scope'
import { SelectField } from './ui/select'
import { Input } from './ui/input'
import { Button } from './ui/button'

export function GitRepositoryField(props: {
  provider: GitProvider
  connectionId: string
  value: string
  onChange: (value: string) => void
  onBranchChange?: (value: string) => void
}) {
  // A connection switch discards pagination and all private metadata from the old selection.
  return <RepositoryField key={`${props.provider}:${props.connectionId}`} {...props} />
}
function RepositoryField({
  provider,
  connectionId,
  value,
  onChange,
  onBranchChange,
}: Parameters<typeof GitRepositoryField>[0]) {
  const [page, setPage] = useState(1)
  const scope = useScope()
  const connections = useGitConnections()
  const selected = connections.data?.items.find((item) => item.id === connectionId)
  const enabled =
    provider === 'github' &&
    selected?.auth_kind === 'github_app' &&
    selected.enabled &&
    selected.configured
  const query = useQuery({
    queryKey: [
      'git-repositories',
      scope.identity,
      scope.project,
      scope.environment,
      connectionId,
      page,
    ],
    queryFn: ({ signal }) =>
      unwrap(
        client.GET('/git/connections/{id}/repositories', {
          signal,
          params: { path: { id: connectionId }, query: { page } },
        }),
      ),
    enabled: Boolean(enabled),
    retry: false,
    staleTime: 15000,
    gcTime: 0,
  })
  return (
    <div className="grid min-w-0 gap-2">
      {enabled && (
        <>
          <SelectField
            label="Repositories available to this connection"
            value={query.data?.items.some((repo) => repo.full_name === value) ? value : ''}
            onValueChange={(name) => {
              if (!name) return
              onChange(name)
              const branch = query.data?.items.find(
                (repo) => repo.full_name === name,
              )?.default_branch
              if (branch) onBranchChange?.(branch)
            }}
            disabled={query.isPending || Boolean(query.error)}
            options={[
              {
                value: '',
                label: query.isPending ? 'Loading repositories…' : 'Choose an installed repository',
                disabled: true,
              },
              ...(query.data?.items || []).map((repo) => ({
                value: repo.full_name,
                label: `${repo.full_name}${repo.private ? ' · Private' : ''}`,
              })),
            ]}
          />
          {query.error && (
            <div role="alert" className="field-help">
              {message(query.error)} <Button onClick={() => void query.refetch()}>Retry</Button>
            </div>
          )}
          {query.data?.items.length === 0 && (
            <p className="field-help">
              No repositories on this page. Check the repositories selected in your GitHub App
              installation.
            </p>
          )}
          {(page > 1 || Boolean(query.data?.next_page)) && (
            <div className="flex flex-wrap items-center gap-2">
              <Button disabled={page === 1 || query.isFetching} onClick={() => setPage(page - 1)}>
                Previous
              </Button>
              <span className="text-sm">Page {page}</span>
              <Button
                disabled={!query.data?.next_page || query.isFetching}
                onClick={() => setPage(query.data!.next_page)}
              >
                Next
              </Button>
            </div>
          )}
        </>
      )}
      <label>
        {enabled ? 'Or enter owner/repository' : 'Repository'}
        <Input
          required
          data-build-source
          value={value}
          onChange={(event) => onChange(event.target.value)}
          onBlur={() => onChange(value.trim().replace(/\.git$/i, ''))}
          placeholder={provider === 'gitlab' ? 'group/subgroup/project' : 'owner/repository'}
          maxLength={provider === 'gitlab' ? 512 : 201}
          autoCapitalize="none"
          autoCorrect="off"
        />
      </label>
      <p className="field-help">
        You can enter a public or unlisted repository manually. Private access and installing a
        build workflow still require permission through this connection.
      </p>
    </div>
  )
}
