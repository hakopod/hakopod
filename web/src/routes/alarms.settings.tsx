import { useState } from 'react'
import { createFileRoute } from '@tanstack/react-router'
import { useQueryClient } from '@tanstack/react-query'
import {
  alarmSearch,
  useAlarmSettings,
  type AlarmScope,
  type AlarmSettings,
  type AlarmSettingsInput,
} from '../lib/alarms'
import { useProjects } from '../lib/projects'
import { useScope } from '../lib/scope'
import { client, unwrap } from '../lib/client'
import { message } from '../lib/api'
import { FormPage, FormSection } from '../components/form-page'
import { Button } from '../components/ui/button'
import { Input } from '../components/ui/input'
import { SelectField } from '../components/ui/select'
import { ErrorState, Loading, Note } from '../components/shared'

export const Route = createFileRoute('/alarms/settings')({
  validateSearch: alarmSearch,
  component: AlarmSettingsPage,
})

function AlarmSettingsPage() {
  const { project, environment, application_id } = Route.useSearch()
  const scope = { project, environment, application_id }
  const navigate = Route.useNavigate()
  const { identity } = useScope()
  const projects = useProjects()
  const settings = useAlarmSettings(scope, Boolean(project || identity.admin))
  const selected = projects.data?.items.find((item) => item.name === project)
  const [version, setVersion] = useState(0)
  return (
    <FormPage
      title="Alarm settings"
      description="Choose how long a problem must persist before it opens an alarm, and whether to email people with access to this scope."
      breadcrumbs={[]}
    >
      <FormSection
        title="Scope"
        description="Application settings override environment settings, which override project settings. Installation settings apply to nodes."
      >
        <div className="alarm-scope-fields">
          <label>
            Project
            <SelectField
              label="Alarm project"
              value={project || ''}
              onValueChange={(value) => void navigate({ search: { project: value || undefined } })}
              options={[
                { value: '', label: identity.admin ? 'Installation nodes' : 'Choose a project' },
                ...(project && !selected ? [{ value: project, label: project }] : []),
                ...(projects.data?.items.map((item) => ({
                  value: item.name,
                  label: item.display_name || item.name,
                })) || []),
              ]}
            />
          </label>
          {project && (
            <label>
              Environment
              <SelectField
                label="Alarm environment"
                value={environment || ''}
                onValueChange={(value) =>
                  void navigate({ search: { project, environment: value || undefined } })
                }
                options={[
                  { value: '', label: 'All environments' },
                  ...(environment &&
                  !selected?.environments.some((item) => item.name === environment)
                    ? [{ value: environment, label: environment }]
                    : []),
                  ...(selected?.environments.map((item) => ({
                    value: item.name,
                    label: item.name,
                  })) || []),
                ]}
              />
            </label>
          )}
        </div>
        {application_id && (
          <p className="field-help">
            Application override: <code>{application_id}</code>
          </p>
        )}
        {projects.error && (
          <ErrorState error={projects.error} retry={() => void projects.refetch()} />
        )}
      </FormSection>
      {!project && !identity.admin ? (
        <Note>Choose a project to view its alarm settings.</Note>
      ) : settings.isPending ? (
        <Loading />
      ) : settings.error ? (
        <ErrorState error={settings.error} retry={() => void settings.refetch()} />
      ) : (
        settings.data && (
          <SettingsEditor
            key={`${project}/${environment}/${application_id}/${version}`}
            scope={scope}
            initial={settings.data}
            onReload={async () => {
              const result = await settings.refetch()
              if (result.error) throw result.error
              setVersion((value) => value + 1)
            }}
          />
        )
      )}
    </FormPage>
  )
}

function SettingsEditor({
  scope,
  initial,
  onReload,
}: {
  scope: AlarmScope
  initial: AlarmSettings
  onReload: () => Promise<void>
}) {
  const cache = useQueryClient()
  const [draft, setDraft] = useState<AlarmSettingsInput>({
    enabled: initial.enabled,
    hold_seconds: initial.hold_seconds,
    email_enabled: initial.email_enabled,
    expected_revision: initial.revision,
  })
  const [hold, setHold] = useState(String(initial.hold_seconds))
  const [review, setReview] = useState(false)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [saved, setSaved] = useState(false)
  const [source, setSource] = useState(initial.source)
  const valid = /^\d+$/.test(hold) && Number(hold) <= 3600
  const change = (next: Partial<AlarmSettingsInput>) => {
    setDraft((value) => ({ ...value, ...next }))
    setReview(false)
    setSaved(false)
  }
  const save = async () => {
    if (busy || !valid) return
    setBusy(true)
    setError('')
    try {
      const result = await unwrap(
        client.PUT('/alarm-settings', {
          params: { query: scope },
          body: { ...draft, hold_seconds: Number(hold) },
        }),
      )
      setDraft({
        enabled: result.enabled,
        hold_seconds: result.hold_seconds,
        email_enabled: result.email_enabled,
        expected_revision: result.revision,
      })
      setSource(result.source)
      setSaved(true)
      setReview(false)
      await cache.invalidateQueries({ queryKey: ['alarm-settings'] })
    } catch (err) {
      setError(message(err))
    } finally {
      setBusy(false)
    }
  }
  return (
    <>
      <FormSection title="Detection and delivery">
        <p className="field-help">
          Using {source} settings · Revision {draft.expected_revision}
          {initial.inherited && !saved ? ' · Saving creates an override for this scope.' : ''}
        </p>
        {!initial.can_manage && (
          <Note>
            You can view these settings. A project administrator can change project or environment
            settings; application overrides require deployment access.
          </Note>
        )}
        <label className="alarm-toggle">
          <Input
            type="checkbox"
            checked={draft.enabled}
            disabled={!initial.can_manage || busy}
            onChange={(event) => change({ enabled: event.target.checked })}
          />
          Detect unhealthy resources
        </label>
        <label className="alarm-hold">
          Hold time in seconds
          <Input
            type="number"
            min={0}
            max={3600}
            step={1}
            value={hold}
            disabled={!initial.can_manage || busy}
            aria-describedby="alarm-hold-help"
            aria-invalid={!valid}
            onChange={(event) => {
              setHold(event.target.value)
              setReview(false)
              setSaved(false)
            }}
          />
        </label>
        <p id="alarm-hold-help" className="field-help">
          0–3600 seconds. The default is 120 seconds to avoid notifying on brief interruptions.
          Alarms are evaluated periodically, so delivery is not instantaneous.
        </p>
        <label className="alarm-toggle">
          <Input
            type="checkbox"
            checked={draft.email_enabled}
            disabled={
              !initial.can_manage || busy || (!initial.email_available && !draft.email_enabled)
            }
            onChange={(event) => change({ email_enabled: event.target.checked })}
          />
          Send alarm and recovery emails
        </label>
        {!initial.email_available && (
          <Note>
            Email is unavailable. An installation administrator must configure SMTP before email can
            be enabled.
          </Note>
        )}
        <p className="field-help">
          Emails go to verified, active accounts that currently have read access to this scope. Node
          alarms go to installation administrators. Dashboard notifications remain available when
          email is off.
        </p>
      </FormSection>
      {error && (
        <div role="alert" className="alarm-save-error">
          <p>{error}</p>
          <p>
            Your changes are still here. If the settings changed elsewhere, reload them before
            saving again.
          </p>
          <Button
            disabled={busy}
            onClick={async () => {
              setBusy(true)
              try {
                await onReload()
              } catch (err) {
                setError(message(err))
              } finally {
                setBusy(false)
              }
            }}
          >
            Discard draft and reload
          </Button>
        </div>
      )}
      {review && (
        <FormSection title="Review changes">
          <p>
            Scope:{' '}
            {[scope.project || 'Installation nodes', scope.environment, scope.application_id]
              .filter(Boolean)
              .join(' / ')}
          </p>
          <p>
            Detection {draft.enabled ? 'enabled' : 'disabled'} · Hold {hold} seconds · Email{' '}
            {draft.email_enabled ? 'enabled' : 'disabled'}
          </p>
          {!draft.enabled && (
            <Note>
              New alarms will stop for this scope. Existing incidents remain in the inbox.
            </Note>
          )}
          {draft.email_enabled && (
            <Note>
              Future alarm and recovery transitions will email eligible accounts in this scope.
            </Note>
          )}
        </FormSection>
      )}
      {saved && <p role="status">Alarm settings saved.</p>}
      {initial.can_manage && (
        <div className="form-footer alarm-settings-footer">
          <span className="dialog-footer-note">
            {valid ? 'Changes apply after saving.' : 'Enter a whole number from 0 to 3600.'}
          </span>
          <div className="toolbar-actions">
            {review && (
              <Button disabled={busy} onClick={() => setReview(false)}>
                Keep editing
              </Button>
            )}
            <Button
              variant="primary"
              disabled={busy || !valid || (draft.email_enabled && !initial.email_available)}
              onClick={() => (review ? void save() : setReview(true))}
            >
              {busy ? 'Saving…' : review ? 'Save settings' : 'Review changes'}
            </Button>
          </div>
        </div>
      )}
    </>
  )
}
