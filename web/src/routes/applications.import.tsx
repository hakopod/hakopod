import { Input } from '../components/ui/input'
import { useState } from 'react'
import { createFileRoute, Link, useNavigate } from '@tanstack/react-router'
import { useQueryClient } from '@tanstack/react-query'
import { client, unwrap } from '../lib/client'
import { message, timestamp } from '../lib/api'
import { useScope } from '../lib/scope'
import type { components } from '../lib/api.generated'
import { FormPage, FormSection, FormHint } from '../components/form-page'
import { ServiceIcon } from '../components/service-icon'
import { DiffTable } from '../components/deploy-dialog'
import { Button } from '../components/ui/button'
import { Empty, ErrorState, Note } from '../components/shared'
export const Route = createFileRoute('/applications/import')({ component: ImportRepository })
function ImportRepository() {
  const scope = useScope()
  const navigate = useNavigate()
  const cache = useQueryClient()
  const [provider, setProvider] = useState<'github' | 'gitlab'>('github')
  const [repository, setRepository] = useState('')
  const [branch, setBranch] = useState('main')
  const [path, setPath] = useState('hakopod.toml')
  const [automatic, setAutomatic] = useState(false)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [plan, setPlan] = useState<components['schemas']['SourceImportPlan'] | null>(null)
  const [key, setKey] = useState('')
  if (!scope.identity.admin)
    return (
      <Empty
        icon="lock"
        title="Administrator access required"
        description="Initial repository imports use the installation’s shared provider credentials. An administrator can create the first application; project members can manage its existing source mapping."
      />
    )
  return (
    <FormPage
      title={plan ? `Review ${plan.spec.name}` : 'Import an application from Git'}
      description={`${scope.project} / ${scope.environment} · Create the application from one committed TOML configuration.`}
      breadcrumbs={[
        { label: 'Applications', to: `/projects/${encodeURIComponent(scope.project)}` },
        { label: 'New application', to: '/applications/new' },
        { label: 'Repository import' },
      ]}
      icon="branch"
      help={
        <>
          <FormHint title="Pick the exact file">
            Branches and configuration paths can differ between environments. A path such as
            deploy/production/hakopod.toml keeps the choice explicit.
          </FormHint>
          <FormHint title="Review an immutable commit">
            The review binds the provider, branch, path, commit, and configuration for 15 minutes.
            Applying it creates the source mapping and initial release together.
          </FormHint>
          <FormHint title="Have source code instead">
            Use a <Link to="/builds/new">source build</Link> when the repository needs to produce an
            image first.
          </FormHint>
        </>
      }
    >
      <form
        onSubmit={async (event) => {
          event.preventDefault()
          if (busy) return
          setBusy(true)
          setError('')
          try {
            if (plan) {
              const deployment = await unwrap(
                client.POST('/sources/deploy', {
                  params: { header: { 'Idempotency-Key': key } },
                  body: { review_token: plan.review_token },
                }),
              )
              void cache.invalidateQueries({ queryKey: ['applications'] })
              void navigate({
                to: '/deployments/$deploymentId',
                params: { deploymentId: deployment.id },
              })
            } else {
              const reviewed = await unwrap(
                client.POST('/sources/plan', {
                  body: {
                    project: scope.project,
                    environment: scope.environment,
                    provider,
                    repository,
                    branch,
                    path,
                    auto_deploy: automatic,
                  },
                }),
              )
              setPlan(reviewed)
              setKey(crypto.randomUUID())
            }
          } catch (err) {
            setError(message(err))
          } finally {
            setBusy(false)
          }
        }}
      >
        <div className="form-body">
          {plan ? (
            <>
              <FormSection title="Pinned repository source" icon="branch">
                <dl className="service-definition-list">
                  <div>
                    <dt>Provider / repository</dt>
                    <dd>
                      {plan.source.provider === 'gitlab' ? 'GitLab' : 'GitHub'} /{' '}
                      {plan.source.repository}
                    </dd>
                  </div>
                  <div>
                    <dt>Branch / configuration path</dt>
                    <dd>
                      {plan.source.branch} / {plan.source.path}
                    </dd>
                  </div>
                  <div>
                    <dt>Commit</dt>
                    <dd className="break-text">
                      <code>{plan.commit_sha}</code>
                    </dd>
                  </div>
                  <div>
                    <dt>Review expires</dt>
                    <dd>{timestamp(plan.expires_at)}</dd>
                  </div>
                  <div>
                    <dt>Automatic deployment</dt>
                    <dd>{plan.source.auto_deploy ? 'Enabled for authenticated pushes' : 'Off'}</dd>
                  </div>
                </dl>
              </FormSection>
              <FormSection title="Initial application revision" icon="box">
                <DiffTable changes={plan.changes} />
                {plan.warnings.map((warning) => (
                  <Note key={warning}>{warning}</Note>
                ))}
              </FormSection>
            </>
          ) : (
            <>
              <FormSection
                title="Repository"
                description="Select the provider and repository that contains your Hakopod configuration."
                icon="branch"
              >
                <div className="provider-picker" role="group" aria-label="Git provider">
                  {(['github', 'gitlab'] as const).map((value) => (
                    <Button
                      type="button"
                      key={value}
                      aria-pressed={provider === value}
                      variant={provider === value ? 'primary' : 'secondary'}
                      onClick={() => setProvider(value)}
                    >
                      <ServiceIcon name={value} size={18} />
                      {value === 'github' ? 'GitHub' : 'GitLab'}
                    </Button>
                  ))}
                </div>
                <label>
                  Repository
                  <Input
                    required
                    value={repository}
                    onChange={(event) => setRepository(event.target.value)}
                    maxLength={201}
                    placeholder={
                      provider === 'gitlab' ? 'group/subgroup/project' : 'owner/repository'
                    }
                    autoCapitalize="none"
                    autoCorrect="off"
                  />
                </label>
                <label>
                  Branch
                  <Input
                    required
                    value={branch}
                    onChange={(event) => setBranch(event.target.value)}
                    maxLength={200}
                    autoCapitalize="none"
                    autoCorrect="off"
                  />
                </label>
                <label>
                  Configuration path
                  <Input
                    required
                    value={path}
                    onChange={(event) => setPath(event.target.value)}
                    maxLength={512}
                    placeholder="deploy/production/hakopod.toml"
                    autoCapitalize="none"
                    autoCorrect="off"
                  />
                </label>
                <p className="field-help">
                  Private repositories use the matching{' '}
                  <Link to="/settings/integrations/$provider" params={{ provider }}>
                    provider integration
                  </Link>
                  .
                </p>
              </FormSection>
              <FormSection title="After the first deployment" icon="refresh">
                <label className="checkbox-row">
                  <Input
                    type="checkbox"
                    checked={automatic}
                    onChange={(event) => setAutomatic(event.target.checked)}
                  />
                  Deploy automatically on authenticated pushes to this branch
                </label>
                {automatic && (
                  <Note>
                    Configure the provider’s push webhook with the saved integration secret.
                    Automatic deployments continue under your current authority.
                  </Note>
                )}
                <Note>
                  The initial TOML must reference container images. Configure custom domains after
                  the application exists so ownership can be verified.
                </Note>
              </FormSection>
            </>
          )}
          {error && <ErrorState error={error} />}
        </div>
        <div className="form-footer">
          {plan ? (
            <Button
              type="button"
              disabled={busy}
              onClick={() => {
                setPlan(null)
                setError('')
              }}
            >
              Back to repository
            </Button>
          ) : (
            <Button asChild>
              <Link to="/applications/new">Cancel</Link>
            </Button>
          )}
          <Button
            variant="primary"
            type="submit"
            disabled={busy || !scope.project || !scope.environment}
          >
            {busy ? 'Working…' : plan ? 'Create reviewed application' : 'Fetch and review TOML'}
          </Button>
        </div>
      </form>
    </FormPage>
  )
}
