export function ManagedActionsTokenHelp({
  organization,
  repository,
}: {
  organization: boolean
  repository: boolean
}) {
  return (
    <div className="grid min-w-0 gap-2 text-sm">
      <p>Supply a fine-grained GitHub token with these permissions:</p>
      <ul className="list-disc space-y-1 pl-5">
        {organization && (
          <li>
            Organization permissions → <strong>Self-hosted runners: Read and write</strong>
          </li>
        )}
        {repository && (
          <li>
            Repository permissions → <strong>Administration: Read and write</strong>
          </li>
        )}
        <li>
          Repository permissions → <strong>Actions: Read-only</strong> for job steps and completed
          logs.
        </li>
      </ul>
      <details>
        <summary className="min-h-11 cursor-pointer py-3">How to create the GitHub token</summary>
        <ol className="list-decimal space-y-3 pb-2 pl-5">
          <li>
            Open{' '}
            <a
              href="https://github.com/settings/personal-access-tokens/new"
              target="_blank"
              rel="noreferrer"
              className="underline underline-offset-4"
            >
              GitHub’s fine-grained token form
            </a>{' '}
            (Settings → Developer settings → Personal access tokens → Fine-grained tokens).
          </li>
          <li>
            Give the token a name and expiration. Set <strong>Resource owner</strong> to the
            organization or account that owns your repositories.
          </li>
          <li>
            Under <strong>Repository access</strong>, select the repositories this pool will run
            jobs for. Include private repositories whose job details and logs you want to see.
          </li>
          <li>
            Under <strong>Permissions</strong>, add the permissions listed above. Runner management
            and Actions read access are separate permissions.
          </li>
          <li>
            Generate and copy the token, then save it as the application secret requested by
            Hakopod. If your organization requires approval, have an owner approve the token.
          </li>
        </ol>
      </details>
    </div>
  )
}
