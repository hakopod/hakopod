import { useRef, useState } from 'react'
import { createFileRoute, Link, useNavigate } from '@tanstack/react-router'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { client, unwrap } from '../lib/client'
import { relative } from '../lib/api'
import { canAccess, useScope } from '../lib/scope'
import { useProjects } from '../lib/projects'
import { gitNames, useGitConnections } from '../lib/git-connections'
import {
  applicationNameFrom,
  defaultEnvironment,
  filledValues,
  migrationActive,
  providerNames,
  type MigrationProvider,
  type MigrationSourceProject,
  type MigrationValues,
  type PlatformMigration,
} from '../lib/platform-migrations'
import { FormHint, FormPage, FormSection } from '../components/form-page'
import { ChoiceCards, FlowSteps, PendingButton, plural } from '../components/create-flow'
import { MigrationProgress, MigrationReview } from '../components/platform-migration'
import { SettingsRow, SettingsRows, SettingsSection } from '../components/settings-section'
import { Empty, ErrorState, Loading, Note, RequestError, Status } from '../components/shared'
import { Button } from '../components/ui/button'
import { Dialog } from '../components/ui/dialog'
import { Input } from '../components/ui/input'
import { SelectField } from '../components/ui/select'

export const Route = createFileRoute('/applications/migrate')({
  validateSearch: (search: Record<string, unknown>): { id?: string } => ({
    id: typeof search.id === 'string' && search.id ? search.id : undefined,
  }),
  component: MigratePage,
})

const steps = ['Source', 'Project', 'Review', 'Progress']

const tokenHelp: Record<MigrationProvider, string> = {
  railway:
    'Create one in Railway under Account Settings → Tokens, as an account or workspace token. A project token also works.',
  vercel: 'Create one in Vercel under Account Settings → Tokens, and choose the team as its scope.',
}

function MigratePage() {
  const scope = useScope()
  const { id } = Route.useSearch()
  if (!scope.can('deployments:write'))
    return (
      <Empty
        icon="lock"
        title="Deployment access required"
        description="Choose a project where you can create applications."
      />
    )
  return (
    <FormPage
      title="Migrate from Railway or Vercel"
      description="Copy a project into a new Hakopod application. The source project is only read."
      breadcrumbs={[
        { label: 'Applications', to: `/projects/${encodeURIComponent(scope.project)}` },
        { label: 'New application', to: '/applications/new' },
        { label: 'Migrate' },
      ]}
      help={
        <>
          <FormHint title="What comes over">
            Services, their variables and secrets, cron schedules, PostgreSQL and Redis data, and
            custom domains.
          </FormHint>
          <FormHint title="What stays behind">
            MySQL and MongoDB data, volume data, preview environments, and Vercel Edge Middleware.
            The review lists anything else.
          </FormHint>
          <FormHint title="Nothing changes at the source">
            Your Railway or Vercel project keeps running. Shut it down yourself once the new
            application works.
          </FormHint>
        </>
      }
    >
      {id ? <MigrationView key={id} id={id} /> : <MigrationSetup />}
    </FormPage>
  )
}

function MigrationSetup() {
  const scope = useScope()
  const navigate = useNavigate()
  const cache = useQueryClient()
  const projects = useProjects()
  const connections = useGitConnections()
  const [step, setStep] = useState<'source' | 'project'>('source')
  const [provider, setProvider] = useState<MigrationProvider>('railway')
  const [token, setToken] = useState('')
  const [sources, setSources] = useState<MigrationSourceProject[]>([])
  const [sourceId, setSourceId] = useState('')
  const [sourceEnvironment, setSourceEnvironment] = useState('')
  const [chosenProject, setProject] = useState(scope.project)
  const [chosenEnvironment, setEnvironment] = useState(scope.environment)
  const [name, setName] = useState('')
  const [connection, setConnection] = useState<string>()
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<unknown>(null)
  const source = sources.find((item) => item.id === sourceId)
  const writable = (projects.data?.items || []).filter((item) =>
    canAccess(scope.identity, item.name, 'deployments:write'),
  )
  // Fall back to the first project and environment this person can deploy to.
  const project = (writable.find((item) => item.name === chosenProject) || writable[0])?.name || ''
  const environments = writable.find((item) => item.name === project)?.environments || []
  const environment =
    (environments.find((item) => item.name === chosenEnvironment) || environments[0])?.name || ''
  const builders = (connections.data?.items || []).filter(
    (item) => item.enabled && item.capabilities.builds,
  )
  const chosenConnection =
    connection ?? (builders.find((item) => item.provider === 'github') || builders[0])?.id ?? ''
  const pickSource = (item?: MigrationSourceProject) => {
    setSourceId(item?.id || '')
    setSourceEnvironment(defaultEnvironment(item) || '')
    setName(applicationNameFrom(item?.name || ''))
  }
  return (
    <>
      <FlowSteps
        steps={steps}
        current={step === 'source' ? 0 : 1}
        disabled={busy}
        onSelect={() => setStep('source')}
      />
      <form
        onSubmit={async (event) => {
          event.preventDefault()
          if (busy) return
          setBusy(true)
          setError(null)
          try {
            if (step === 'source') {
              const result = await unwrap(
                client.POST('/migrations/projects', { body: { provider, token } }),
              )
              if (!result.projects.length)
                throw new Error(
                  `This token cannot read any ${providerNames[provider]} projects. Check its scope.`,
                )
              setSources(result.projects)
              if (!result.projects.some((item) => item.id === sourceId))
                pickSource(result.projects[0])
              setStep('project')
            } else {
              const created = await unwrap(
                client.POST('/migrations', {
                  body: {
                    provider,
                    token,
                    source_project_id: sourceId,
                    source_environment_id:
                      provider === 'railway' ? sourceEnvironment || undefined : undefined,
                    source_team_id: source?.team_id || undefined,
                    project,
                    environment,
                    application_name: name,
                    git_connection_id: chosenConnection || undefined,
                  },
                }),
              )
              setToken('')
              cache.setQueryData(['platform-migration', created.id], created)
              void cache.invalidateQueries({ queryKey: ['platform-migrations'] })
              void navigate({ to: '/applications/migrate', search: { id: created.id } })
            }
          } catch (err) {
            setError(err)
          } finally {
            setBusy(false)
          }
        }}
      >
        <div className="form-body create-flow migration-flow">
          {step === 'source' ? (
            <FormSection title="Source">
              <ChoiceCards
                compact
                label="Where is the project now?"
                value={provider}
                disabled={busy}
                onChange={(value) => {
                  setProvider(value)
                  setSources([])
                  pickSource()
                }}
                choices={[
                  {
                    value: 'railway',
                    title: 'Railway',
                    description: 'Services, databases, variables, cron schedules and domains.',
                  },
                  {
                    value: 'vercel',
                    title: 'Vercel',
                    description: 'Projects built from Git, their variables, crons and domains.',
                  },
                ]}
              />
              <label>
                {providerNames[provider]} token
                <Input
                  type="password"
                  required
                  minLength={8}
                  maxLength={4096}
                  autoComplete="off"
                  spellCheck={false}
                  value={token}
                  onChange={(event) => setToken(event.target.value)}
                />
                <span className="field-help">{tokenHelp[provider]}</span>
              </label>
              <Note>
                Hakopod uses this token only for this migration, and only to read. It is erased when
                the migration ends, or after 24 hours.
              </Note>
            </FormSection>
          ) : (
            <>
              <FormSection title={`${providerNames[provider]} project`}>
                <div className="create-fields">
                  <label className="min-w-0">
                    Project
                    <SelectField
                      label="Project"
                      required
                      value={sourceId}
                      onValueChange={(value) =>
                        pickSource(sources.find((item) => item.id === value))
                      }
                      options={sources.map((item) => ({
                        value: item.id,
                        label: item.team ? `${item.team} / ${item.name}` : item.name,
                      }))}
                    />
                  </label>
                  {provider === 'railway' && Boolean(source?.environments?.length) && (
                    <label className="min-w-0">
                      Environment
                      <SelectField
                        label="Environment"
                        required
                        value={sourceEnvironment}
                        onValueChange={setSourceEnvironment}
                        options={(source?.environments || []).map((item) => ({
                          value: item.id,
                          label: item.name,
                        }))}
                      />
                    </label>
                  )}
                </div>
              </FormSection>
              <FormSection title="On Hakopod">
                <div className="create-fields">
                  <label className="min-w-0">
                    Project
                    <SelectField
                      label="Hakopod project"
                      required
                      value={project}
                      onValueChange={setProject}
                      options={writable.map((item) => ({
                        value: item.name,
                        label: item.display_name || item.name,
                      }))}
                    />
                  </label>
                  <label className="min-w-0">
                    Environment
                    <SelectField
                      label="Hakopod environment"
                      required
                      value={environment}
                      onValueChange={setEnvironment}
                      options={environments.map((item) => ({ value: item.name, label: item.name }))}
                    />
                  </label>
                  <label className="min-w-0">
                    Application name
                    <Input
                      required
                      value={name}
                      pattern="[a-z](?:[a-z0-9\-]{0,38}[a-z0-9])?"
                      maxLength={40}
                      autoCapitalize="none"
                      autoCorrect="off"
                      onChange={(event) => setName(event.target.value)}
                    />
                    <span className="field-help">Lowercase letters, numbers and dashes.</span>
                  </label>
                  {connections.data && (
                    <label className="min-w-0">
                      Git connection for builds
                      <SelectField
                        label="Git connection for builds"
                        value={chosenConnection}
                        onValueChange={setConnection}
                        options={[
                          { value: '', label: 'None' },
                          ...builders.map((item) => ({
                            value: item.id,
                            label: `${gitNames[item.provider as 'github' | 'gitlab'] || item.provider} · ${item.name}`,
                          })),
                        ]}
                      />
                      <span className="field-help">
                        Needed when a service builds from GitHub. Hakopod opens a pull request
                        there.
                      </span>
                    </label>
                  )}
                </div>
              </FormSection>
            </>
          )}
          {error !== null && <RequestError error={error} />}
        </div>
        <div className="form-footer">
          {step === 'project' ? (
            <Button type="button" disabled={busy} onClick={() => setStep('source')}>
              Back
            </Button>
          ) : (
            <Button asChild>
              <Link to="/applications/new">Cancel</Link>
            </Button>
          )}
          <PendingButton
            variant="primary"
            type="submit"
            pending={busy}
            pendingLabel={step === 'source' ? 'Loading projects…' : 'Reading the project…'}
            disabled={
              step === 'source' ? token.length < 8 : !sourceId || !project || !environment || !name
            }
          >
            {step === 'source' ? 'Load projects' : 'Review migration'}
          </PendingButton>
        </div>
      </form>
      {step === 'source' && <RecentMigrations />}
    </>
  )
}

function RecentMigrations() {
  const scope = useScope()
  const list = useQuery({
    queryKey: ['platform-migrations', scope.project, scope.environment],
    queryFn: ({ signal }) =>
      unwrap(
        client.GET('/migrations', {
          signal,
          params: { query: { project: scope.project, environment: scope.environment } },
        }),
      ),
    enabled: Boolean(scope.project && scope.environment),
    retry: false,
  })
  const items = list.data?.items.slice(0, 10) || []
  if (!items.length) return null
  return (
    <SettingsSection
      title="Recent migrations"
      state={`${scope.project} / ${scope.environment}`}
      className="mt-6"
    >
      <SettingsRows label="Recent migrations">
        {items.map((item) => (
          <SettingsRow
            key={item.id}
            name={item.application_name}
            detail={`From ${providerNames[item.provider]} / ${item.source_project_name || item.source_project_id}`}
            status={<Status value={item.status} small />}
            meta={relative(item.created_at)}
            actions={
              <Button asChild size="sm">
                <Link to="/applications/migrate" search={{ id: item.id }}>
                  Open
                </Link>
              </Button>
            }
          />
        ))}
      </SettingsRows>
    </SettingsSection>
  )
}

function MigrationView({ id }: { id: string }) {
  const navigate = useNavigate()
  const cache = useQueryClient()
  const migration = useQuery({
    queryKey: ['platform-migration', id],
    queryFn: ({ signal }) =>
      unwrap(client.GET('/migrations/{id}', { signal, params: { path: { id } } })),
    refetchInterval: (query) => (migrationActive(query.state.data?.status) ? 3000 : false),
    retry: false,
  })
  const [values, setValues] = useState<MigrationValues>({})
  const [busy, setBusy] = useState('')
  const [error, setError] = useState<unknown>(null)
  const [confirming, setConfirming] = useState(false)
  const startKey = useRef('')
  const update = (next: PlatformMigration) => {
    cache.setQueryData(['platform-migration', id], next)
    void cache.invalidateQueries({ queryKey: ['platform-migrations'] })
  }
  const run = async (label: string, action: () => Promise<void>) => {
    setBusy(label)
    setError(null)
    try {
      await action()
    } catch (err) {
      setError(err)
    } finally {
      setBusy('')
    }
  }
  if (migration.isPending)
    return (
      <>
        <FlowSteps steps={steps} current={2} />
        <Loading />
      </>
    )
  if (migration.error)
    return <ErrorState error={migration.error} retry={() => void migration.refetch()} />
  const data = migration.data
  const proposal = data.proposal
  const planned = data.status === 'planned'
  const blocked = proposal.missing.length > 0
  const copies = proposal.databases.filter((item) => item.copy_data).length
  const cancel = () =>
    run('cancel', async () => {
      update(await unwrap(client.POST('/migrations/{id}/cancel', { params: { path: { id } } })))
    })
  return (
    <>
      <FlowSteps
        steps={steps}
        current={planned ? 2 : 3}
        onSelect={planned ? () => void navigate({ to: '/applications/migrate' }) : undefined}
      />
      <div className="form-body create-flow migration-flow">
        {planned ? (
          <MigrationReview
            migration={data}
            values={values}
            saving={busy === 'save'}
            onValue={(service, name, value) =>
              setValues((current) => ({
                ...current,
                [service]: { ...current[service], [name]: value },
              }))
            }
            onSave={() =>
              void run('save', async () => {
                update(
                  await unwrap(
                    client.PUT('/migrations/{id}/variables', {
                      params: { path: { id } },
                      body: { values: filledValues(values) },
                    }),
                  ),
                )
                setValues({})
              })
            }
          />
        ) : (
          <MigrationProgress migration={data} />
        )}
        {data.cancel_requested && migrationActive(data.status) && (
          <Note>Cancelling. The migration stops after its current step.</Note>
        )}
        {error !== null && <RequestError error={error} />}
      </div>
      <div className="form-footer">
        {planned || migrationActive(data.status) ? (
          <PendingButton
            type="button"
            pending={busy === 'cancel'}
            pendingLabel="Cancelling…"
            disabled={Boolean(busy) || data.cancel_requested}
            onClick={() => void cancel()}
          >
            Cancel migration
          </PendingButton>
        ) : (
          <Button asChild>
            <Link to="/applications/migrate">New migration</Link>
          </Button>
        )}
        {planned && (
          <Button
            variant="primary"
            type="button"
            disabled={Boolean(busy) || blocked}
            onClick={() => setConfirming(true)}
          >
            {blocked ? `${plural(proposal.missing.length, 'value')} missing` : 'Start migration'}
          </Button>
        )}
      </div>
      <Dialog
        open={confirming}
        onOpenChange={(open) => !busy && setConfirming(open)}
        title={`Start migrating to ${data.application_name}?`}
        description={`Hakopod creates the application in ${data.project} / ${data.environment}. The ${providerNames[data.provider]} project is not changed.`}
      >
        <div className="dialog-body">
          <ul className="m-0 grid list-disc gap-1.5 pl-5">
            <li>{plural(Object.keys(proposal.spec.services).length, 'service')}</li>
            {proposal.databases.length > 0 && (
              <li>
                {plural(proposal.databases.length, 'database')}
                {copies > 0 && `, with data copied into ${copies}`}
              </li>
            )}
            {proposal.secret_names.length > 0 && (
              <li>{plural(proposal.secret_names.length, 'secret')}</li>
            )}
            {proposal.builds.length > 0 && (
              <li>{plural(proposal.builds.length, 'build pull request')}</li>
            )}
            {Object.keys(proposal.domains).length > 0 && (
              <li>
                {plural(Object.keys(proposal.domains).length, 'domain')}, pending until DNS points
                at Hakopod
              </li>
            )}
          </ul>
          {error !== null && <RequestError error={error} />}
        </div>
        <div className="dialog-footer">
          <Button type="button" disabled={Boolean(busy)} onClick={() => setConfirming(false)}>
            Not yet
          </Button>
          <PendingButton
            variant="primary"
            pending={busy === 'start'}
            pendingLabel="Starting…"
            onClick={() =>
              void run('start', async () => {
                if (!startKey.current) startKey.current = crypto.randomUUID()
                update(
                  await unwrap(
                    client.POST('/migrations/{id}/start', {
                      params: { path: { id }, header: { 'Idempotency-Key': startKey.current } },
                    }),
                  ),
                )
                setConfirming(false)
              })
            }
          >
            Start migration
          </PendingButton>
        </div>
      </Dialog>
    </>
  )
}
