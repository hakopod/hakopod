import { useState } from 'react'
import { Link, useNavigate } from '@tanstack/react-router'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { client, unwrap } from '../lib/client'
import { APIError, message } from '../lib/api'
import {
  prepareSMTP,
  type SMTPSettings as SMTPConfiguration,
  type SecretAction,
} from '../lib/installation-settings'
import { InstallationAccess } from './installation-access'
import { FormPage, FormSection } from './form-page'
import {
  InstallationCredential,
  InstallationReviewRows,
  credentialSummary,
  useInstallationFormFocus,
} from './installation-form-fields'
import { Button } from './ui/button'
import { Input } from './ui/input'
import { SelectField } from './ui/select'
import { ErrorState, Loading, Note, RequestError } from './shared'
import { SettingsSection } from './settings-section'
import { GuidedEmpty } from './resource-card'

function useSMTP() {
  return useQuery({
    queryKey: ['installation-smtp'],
    queryFn: ({ signal }) => unwrap(client.GET('/installation/smtp', { signal })),
    staleTime: 30000,
    refetchOnWindowFocus: false,
    retry: false,
  })
}
export function SMTPSettingsPanel() {
  return (
    <InstallationAccess>
      <SMTPOverview />
    </InstallationAccess>
  )
}
function SMTPOverview() {
  const query = useSMTP()
  const [busy, setBusy] = useState(false),
    [sent, setSent] = useState(''),
    [error, setError] = useState('')
  const help =
    'Send invitations, password recovery messages and alarm notifications through your SMTP server. The test email uses saved settings and goes to your administrator email address.'
  if (query.isPending || query.error || !query.data)
    return (
      <SettingsSection title="Email delivery" help={help}>
        {query.isPending ? (
          <Loading />
        ) : (
          <ErrorState error={query.error} retry={() => void query.refetch()} />
        )}
      </SettingsSection>
    )
  const current = query.data
  const configured = Boolean(current.host && current.from_email)
  // Without the encryption key nothing can be saved, so no edit action is offered.
  const editable = current.encryption_ready
  const configure = editable && (
    <Button variant={configured ? 'outline' : 'primary'} asChild>
      <Link to="/settings/smtp">{configured ? 'Edit SMTP' : 'Configure SMTP'}</Link>
    </Button>
  )
  return (
    <SettingsSection
      title="Email delivery"
      help={help}
      state={
        !editable
          ? 'Locked · set the installation encryption key before configuring SMTP'
          : !configured
            ? 'Not configured · Hakopod sends no email'
            : current.enabled
              ? `Enabled · sends from ${current.from_email} through ${current.host}:${current.port}`
              : `Disabled · ${current.host}:${current.port} is saved but not used`
      }
      actions={
        <>
          {current.enabled && (
            <Button
              disabled={busy}
              aria-busy={busy || undefined}
              onClick={async () => {
                if (busy) return
                setBusy(true)
                setError('')
                setSent('')
                try {
                  const result = await unwrap(
                    client.POST('/installation/smtp/test', {
                      body: { expected_revision: current.revision },
                    }),
                  )
                  setSent(`Test email sent to ${result.recipient}.`)
                } catch (cause) {
                  setError(message(cause))
                  if (cause instanceof APIError && cause.status === 409) void query.refetch()
                } finally {
                  setBusy(false)
                }
              }}
            >
              {busy ? 'Sending…' : 'Send test email'}
            </Button>
          )}
          {configured && configure}
        </>
      }
    >
      {sent && (
        <p role="status" className="m-0 text-sm">
          {sent}
        </p>
      )}
      {error && <RequestError error={error} />}
      {!configured ? (
        <GuidedEmpty
          compact
          icon="info"
          title="Send email from this installation"
          description="Invitations, password recovery and alarm email need an SMTP server."
          primary={configure || undefined}
        />
      ) : (
        <InstallationReviewRows
          rows={[
            ['SMTP server', current.host ? `${current.host}:${current.port}` : 'Not configured'],
            ...(current.host
              ? [
                  ['Connection', current.security === 'tls' ? 'TLS' : 'STARTTLS'] as [
                    string,
                    string,
                  ],
                ]
              : []),
            ['Sender', current.from_email || 'Not configured'],
            [
              'Authentication',
              current.username
                ? `${current.username} · password ${current.password_set ? 'stored' : 'not set'}`
                : 'None',
            ],
            [
              'Configuration',
              current.source === 'operator' ? 'Operator settings' : 'Dashboard settings',
            ],
          ]}
        />
      )}
    </SettingsSection>
  )
}
export function SMTPEditor() {
  return (
    <InstallationAccess>
      <SMTPLoader />
    </InstallationAccess>
  )
}
function SMTPLoader() {
  const query = useSMTP()
  if (query.isPending) return <Loading />
  if (query.error || !query.data)
    return <ErrorState error={query.error} retry={() => void query.refetch()} />
  return <SMTPForm current={query.data} />
}
function SMTPForm({ current }: { current: SMTPConfiguration }) {
  const [draft, setDraft] = useState(current)
  const [action, setAction] = useState<SecretAction>('keep'),
    [password, setPassword] = useState('')
  const [review, setReview] = useState(false)
  const formRef = useInstallationFormFocus(review)
  const [busy, setBusy] = useState(false)
  const [conflict, setConflict] = useState(false)
  const [error, setError] = useState('')
  const navigate = useNavigate(),
    cache = useQueryClient()
  const change = <K extends keyof SMTPConfiguration>(key: K, value: SMTPConfiguration[K]) =>
    setDraft((old) => ({ ...old, [key]: value }))
  async function save() {
    if (busy || conflict || !current.encryption_ready) return
    setBusy(true)
    setError('')
    try {
      const result = await unwrap(
        client.PUT('/installation/smtp', { body: prepareSMTP(draft, action, password) }),
      )
      setPassword('')
      cache.setQueryData(['installation-smtp'], result)
      await cache.invalidateQueries({ queryKey: ['auth-status'] })
      await navigate({ to: '/settings', search: { tab: 'smtp' } })
    } catch (cause) {
      setError(message(cause))
      if (cause instanceof APIError && cause.status === 409) {
        setConflict(true)
        void cache.invalidateQueries({ queryKey: ['installation-smtp'] })
      }
    } finally {
      setBusy(false)
    }
  }
  return (
    <FormPage
      title="Configure email delivery"
      description="Save an SMTP server for account and alarm email, then send a test from Settings."
      breadcrumbs={[]}
    >
      <form
        ref={formRef}
        tabIndex={-1}
        aria-label={review ? 'Email delivery review' : 'Email delivery configuration'}
        className="settings-task-form"
        onSubmit={(event) => {
          event.preventDefault()
          if (busy) return
          if (review) {
            void save()
            return
          }
          try {
            prepareSMTP(draft, action, password)
            setError('')
            setReview(true)
          } catch (cause) {
            setError(message(cause))
          }
        }}
      >
        {(!current.encryption_ready || current.source === 'operator') && (
          <Note>
            {!current.encryption_ready &&
              'Configure the installation encryption key before saving SMTP settings. '}
            {current.source === 'operator' &&
              'These values come from operator configuration. Saving creates a dashboard override, and disabling delivery here also disables delivery from operator settings.'}
          </Note>
        )}
        {review ? (
          <FormSection title="Review email delivery">
            <InstallationReviewRows
              rows={[
                ['Delivery', draft.enabled ? 'Enabled' : 'Disabled'],
                ['SMTP host', draft.host || 'None'],
                ['Port', String(draft.port)],
                ['Connection', draft.security === 'tls' ? 'TLS' : 'STARTTLS'],
                ['Sender', draft.from_email || 'None'],
                ['Username', draft.username || 'No authentication'],
                ['Password', credentialSummary(action, current.password_set)],
                ['Revision', String(draft.revision)],
              ]}
            />
            <p className="field-help">
              These settings apply to invitation, password recovery and alarm email. Saving does not
              send a test message.
            </p>
          </FormSection>
        ) : (
          <>
            <FormSection title="Delivery">
              <label className="checkbox-label">
                <Input
                  type="checkbox"
                  checked={draft.enabled}
                  onChange={(event) => change('enabled', event.target.checked)}
                />
                Enable email delivery
              </label>
              <label>
                Sender email
                <Input
                  type="email"
                  required={draft.enabled}
                  value={draft.from_email}
                  maxLength={254}
                  placeholder="notifications@example.com"
                  onChange={(event) => change('from_email', event.target.value)}
                />
              </label>
            </FormSection>
            <FormSection title="SMTP server">
              <div className="form-grid-two">
                <label>
                  Host
                  <Input
                    required={draft.enabled}
                    value={draft.host}
                    maxLength={253}
                    placeholder="smtp.example.com"
                    onChange={(event) => change('host', event.target.value)}
                    spellCheck={false}
                  />
                </label>
                <label>
                  Port
                  <Input
                    type="number"
                    required
                    min={1}
                    max={65535}
                    value={Number.isNaN(draft.port) ? '' : draft.port}
                    onChange={(event) => change('port', event.target.valueAsNumber)}
                  />
                </label>
              </div>
              <label>
                Connection security
                <SelectField
                  label="Connection security"
                  value={draft.security}
                  onValueChange={(value) =>
                    change('security', value as SMTPConfiguration['security'])
                  }
                  options={[
                    { value: 'starttls', label: 'STARTTLS' },
                    { value: 'tls', label: 'TLS' },
                  ]}
                />
                <small className="field-help">
                  Use the security mode and port specified by your mail provider. Certificate
                  verification is always enabled.
                </small>
              </label>
            </FormSection>
            <FormSection title="Authentication">
              <label>
                Username (optional)
                <Input
                  value={draft.username}
                  maxLength={256}
                  onChange={(event) => change('username', event.target.value)}
                  autoComplete="off"
                />
              </label>
              <InstallationCredential
                label="SMTP password"
                maxLength={4096}
                configured={current.password_set}
                action={action}
                onAction={setAction}
                value={password}
                onValue={setPassword}
              />
            </FormSection>
          </>
        )}
        {error && <RequestError error={error} />}
        {conflict && (
          <Note>
            Another administrator changed email settings. Your draft is kept. Return to settings and
            reopen this form to review the latest revision.
          </Note>
        )}
        <div className="form-footer">
          <Button
            type="button"
            variant="outline"
            disabled={busy}
            onClick={() =>
              review
                ? setReview(false)
                : void navigate({ to: '/settings', search: { tab: 'smtp' } })
            }
          >
            {review ? 'Back to edit' : 'Cancel'}
          </Button>
          <Button
            type="submit"
            variant="primary"
            disabled={busy || conflict || !current.encryption_ready}
          >
            {busy ? 'Saving…' : review ? 'Save SMTP settings' : 'Review changes'}
          </Button>
        </div>
      </form>
    </FormPage>
  )
}
