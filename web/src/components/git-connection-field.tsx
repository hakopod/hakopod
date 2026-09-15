import { useScope } from '../lib/scope'
import {
  gitConnectionOptions,
  gitNames,
  useGitConnections,
  type GitProvider,
} from '../lib/git-connections'
import { ErrorState } from './shared'
import { SelectField } from './ui/select'
export function GitConnectionField({
  provider,
  value,
  onValueChange,
  builds = false,
}: {
  provider: GitProvider
  value: string
  onValueChange: (value: string) => void
  builds?: boolean
}) {
  const scope = useScope()
  const query = useGitConnections()
  if (!(scope.identity.admin || scope.identity.can_manage_git))
    return (
      <p className="field-help">
        Connection: {value || `${gitNames[provider]} default`}. An administrator manages repository
        access.
      </p>
    )
  return (
    <div className="grid gap-2">
      <label>
        Repository connection
        <SelectField
          label="Repository connection"
          value={value === `${provider}-default` ? '' : value}
          onValueChange={onValueChange}
          disabled={query.isPending || Boolean(query.error)}
          options={gitConnectionOptions(query.data?.items || [], provider, value, builds)}
        />
      </label>
      {query.error ? (
        <ErrorState error={query.error} />
      ) : (
        <p className="field-help">
          This connection controls repository access.{' '}
          <a href="/settings?tab=github" target="_blank" rel="noopener noreferrer">
            Manage connections in a new tab
          </a>
        </p>
      )}
    </div>
  )
}
