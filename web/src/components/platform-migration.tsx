import { Link } from '@tanstack/react-router'
import {
  providerNames,
  stepNames,
  stillMissing,
  type MigrationValues,
  type PlatformMigration,
} from '../lib/platform-migrations'
import { FormSection } from './form-page'
import { SettingsRow, SettingsRows, SettingsState } from './settings-section'
import { Note, Status } from './shared'
import { Button } from './ui/button'
import { Input } from './ui/input'
import { plural } from './create-flow'

const engineNames: Record<string, string> = {
  postgresql: 'PostgreSQL',
  postgres: 'PostgreSQL',
  redis: 'Redis',
  mysql: 'MySQL',
  mongodb: 'MongoDB',
}

function Summary({ migration }: { migration: PlatformMigration }) {
  return (
    <div className="review-summary flex-wrap !gap-x-10 !gap-y-4">
      <div>
        <span className="muted-text">FROM</span>
        <strong>
          {providerNames[migration.provider]} / {migration.source_project_name || 'project'}
        </strong>
      </div>
      <div>
        <span className="muted-text">TO</span>
        <strong>
          {migration.project} / {migration.environment} / {migration.application_name}
        </strong>
      </div>
      <div>
        <span className="muted-text">SERVICES</span>
        <strong>{Object.keys(migration.proposal.spec.services).length}</strong>
      </div>
    </div>
  )
}

export function MigrationReview({
  migration,
  values,
  onValue,
  onSave,
  saving,
}: {
  migration: PlatformMigration
  values: MigrationValues
  onValue: (service: string, name: string, value: string) => void
  onSave: () => void
  saving: boolean
}) {
  const proposal = migration.proposal
  const services = Object.entries(proposal.spec.services)
  const secrets = services.filter(([, service]) => Object.keys(service.secrets || {}).length)
  const domains = Object.entries(proposal.domains)
  const missingServices = [...new Set(proposal.missing.map((item) => item.service))]
  const unsaved = stillMissing(proposal, values).length < proposal.missing.length
  return (
    <>
      <Summary migration={migration} />
      {proposal.missing.length > 0 && (
        <FormSection title="Missing values">
          <p className="m-0">
            {providerNames[migration.provider]} does not return the values of sensitive variables.
            Paste them here before you start.
          </p>
          {missingServices.map((service) => (
            <fieldset key={service} className="grid min-w-0 gap-4 border-0 p-0" disabled={saving}>
              <legend className="create-label mb-2 p-0">{service}</legend>
              {proposal.missing
                .filter((item) => item.service === service)
                .map((item) => (
                  <label key={item.name} className="min-w-0">
                    <span className="mono break-all">{item.name}</span>
                    <Input
                      type="password"
                      autoComplete="off"
                      spellCheck={false}
                      value={values[service]?.[item.name] || ''}
                      onChange={(event) => onValue(service, item.name, event.target.value)}
                    />
                  </label>
                ))}
            </fieldset>
          ))}
          <Button
            type="button"
            className="justify-self-start"
            disabled={saving || !unsaved}
            onClick={onSave}
          >
            {saving ? 'Saving…' : 'Save values'}
          </Button>
        </FormSection>
      )}
      <FormSection title="Services">
        <SettingsRows label="Services">
          {services.map(([name, service]) => {
            const build = proposal.builds.find((item) => item.service === name)
            const cron = service.job?.schedule?.cron
            const root = build?.context_path && build.context_path !== '.' ? build.context_path : ''
            return (
              <SettingsRow
                key={name}
                name={name}
                detail={
                  build ? (
                    <>
                      GitHub {build.repository}@{build.branch}
                      {root && ` · root ${root}`}
                    </>
                  ) : (
                    <span className="break-all">{service.image.split('@')[0]}</span>
                  )
                }
                status={
                  cron ? (
                    <SettingsState tone="muted">Cron</SettingsState>
                  ) : service.public ? (
                    <SettingsState tone="ok">Public</SettingsState>
                  ) : (
                    <SettingsState tone="muted">Private</SettingsState>
                  )
                }
                meta={
                  cron ? (
                    <span>
                      Runs <code>{cron}</code>
                    </span>
                  ) : (
                    <span>
                      {service.port ? `Port ${service.port} · ` : ''}
                      {plural(service.replicas ?? 1, 'replica')}
                    </span>
                  )
                }
              />
            )
          })}
        </SettingsRows>
      </FormSection>
      {proposal.builds.length > 0 && (
        <FormSection title="Builds">
          <p className="m-0">
            Hakopod opens a pull request in each repository that adds the build workflow. Each of
            these services starts after its first build, so merge the pull requests to bring them
            up.
          </p>
          <SettingsRows label="Builds">
            {proposal.builds.map((build) => (
              <SettingsRow
                key={build.service}
                name={build.service}
                detail={`Pull request in ${build.repository}, branch ${build.branch}`}
                meta={build.preset || build.mode || undefined}
              />
            ))}
          </SettingsRows>
          {proposal.builds.some((build) => !build.connection_id) && (
            <Note>
              No Git connection was chosen, so the pull requests cannot be opened. Start over and
              choose a Git connection that can run builds.
            </Note>
          )}
        </FormSection>
      )}
      {proposal.databases.length > 0 && (
        <FormSection title="Databases">
          <SettingsRows label="Databases">
            {proposal.databases.map((database) => (
              <SettingsRow
                key={database.name}
                name={database.name}
                detail={`${engineNames[database.engine] || database.engine}${database.version ? ` ${database.version}` : ''}`}
                status={
                  database.copy_data ? (
                    <SettingsState tone="ok">Data copied</SettingsState>
                  ) : (
                    <SettingsState tone="warn">Created empty</SettingsState>
                  )
                }
                meta={database.copy_data ? undefined : database.reason}
              />
            ))}
          </SettingsRows>
        </FormSection>
      )}
      {secrets.length > 0 && (
        <FormSection title="Secrets">
          <p className="m-0">
            These variables are saved as application secrets. Their values are never shown.
          </p>
          <SettingsRows label="Secrets">
            {secrets.map(([name, service]) => (
              <SettingsRow
                key={name}
                name={name}
                detail={
                  <span className="break-all">{Object.keys(service.secrets || {}).join(', ')}</span>
                }
              />
            ))}
          </SettingsRows>
        </FormSection>
      )}
      {domains.length > 0 && (
        <FormSection title="Domains">
          <p className="m-0">
            Domains are added as pending. They go live once their DNS points at Hakopod.
          </p>
          <SettingsRows label="Domains">
            {domains.map(([host, service]) => (
              <SettingsRow
                key={host}
                name={host}
                detail={service}
                status={<SettingsState tone="warn">Pending DNS</SettingsState>}
              />
            ))}
          </SettingsRows>
        </FormSection>
      )}
      {proposal.warnings.length > 0 && (
        <FormSection title="Things to check">
          <ul className="m-0 grid list-disc gap-2 pl-5">
            {proposal.warnings.map((warning) => (
              <li key={warning}>{warning}</li>
            ))}
          </ul>
        </FormSection>
      )}
    </>
  )
}

export function MigrationProgress({ migration }: { migration: PlatformMigration }) {
  const domains = Object.keys(migration.proposal.domains)
  return (
    <>
      <Summary migration={migration} />
      {migration.status === 'succeeded' && (
        <FormSection title="Migration finished">
          <p className="m-0">
            Your application is running on Hakopod. The {providerNames[migration.provider]} project
            was not changed.
          </p>
          <ol className="m-0 grid list-decimal gap-2 pl-5">
            {migration.proposal.builds.length > 0 && (
              <li>
                Merge the build pull requests in{' '}
                {[...new Set(migration.proposal.builds.map((build) => build.repository))].join(
                  ', ',
                )}
                .
              </li>
            )}
            {domains.length > 0 && <li>Point DNS for {domains.join(', ')} at Hakopod.</li>}
            <li>Open the application and check that it works.</li>
            <li>Shut down the {providerNames[migration.provider]} project when you are sure.</li>
          </ol>
          {migration.application_id && (
            <Button asChild variant="primary" className="justify-self-start">
              <Link
                to="/applications/$applicationId"
                params={{ applicationId: migration.application_id }}
              >
                Open {migration.application_name}
              </Link>
            </Button>
          )}
        </FormSection>
      )}
      {(migration.status === 'failed' || migration.status === 'cancelled') && (
        <FormSection
          title={migration.status === 'failed' ? 'Migration failed' : 'Migration cancelled'}
        >
          {migration.error && (
            <p role="alert" className="m-0 text-destructive [overflow-wrap:anywhere]">
              {migration.error}
            </p>
          )}
          <p className="m-0">
            Anything already created on Hakopod was kept. The {providerNames[migration.provider]}{' '}
            project was not changed.
          </p>
          {migration.application_id && (
            <Button asChild className="justify-self-start">
              <Link
                to="/applications/$applicationId"
                params={{ applicationId: migration.application_id }}
              >
                Open {migration.application_name}
              </Link>
            </Button>
          )}
        </FormSection>
      )}
      <FormSection title="Steps">
        <SettingsRows label="Migration steps">
          {migration.steps.map((step) => (
            <SettingsRow
              key={step.name}
              name={stepNames[step.name]}
              detail={step.message}
              status={<Status value={step.status} small />}
              meta={
                step.items?.length ? (
                  <ul className="m-0 grid list-none gap-0.5 p-0">
                    {step.items.map((item) => (
                      <li key={item.name}>
                        {item.name} · {item.status}
                        {item.message && ` · ${item.message}`}
                      </li>
                    ))}
                  </ul>
                ) : undefined
              }
            />
          ))}
        </SettingsRows>
      </FormSection>
    </>
  )
}
