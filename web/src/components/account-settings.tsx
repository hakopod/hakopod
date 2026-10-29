import { Input } from './ui/input'
import { Link } from '@tanstack/react-router'
import { Avatar } from './avatar'
import { useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { client, unwrap } from '../lib/client'
import { message, timestamp } from '../lib/api'
import { passkeyCredential } from '../lib/webauthn'
import { useScope } from '../lib/scope'
import { Button } from './ui/button'
import { Dialog } from './ui/dialog'
import { Copy, ErrorState, Loading, Note, RequestError } from './shared'
import { Icon } from './icons'
import { focusedDialog, plural, ReviewFacts } from './operation-review'
import {
  SettingsEmpty,
  SettingsRow,
  SettingsRows,
  SettingsSection,
  SettingsState,
} from './settings-section'
import type { components } from '../lib/api.generated'

type Session = components['schemas']['HumanSession']

export default function AccountSettings() {
  const { identity } = useScope()
  const cache = useQueryClient()
  const [action, setAction] = useState<'totp' | 'disable' | 'passkey' | null>(null)
  const [remove, setRemove] = useState<{ id: string; name: string } | null>(null)
  const [revoke, setRevoke] = useState<Session | null>(null)
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  const [verifyCode, setVerifyCode] = useState('')
  const [verifyError, setVerifyError] = useState('')
  const security = useQuery({
    queryKey: ['account-security'],
    queryFn: ({ signal }) => unwrap(client.GET('/auth/security', { signal })),
    staleTime: 30000,
    gcTime: 0,
  })
  const sessions = useQuery({
    queryKey: ['account-sessions'],
    queryFn: ({ signal }) => unwrap(client.GET('/auth/sessions', { signal })),
    staleTime: 30000,
    gcTime: 0,
  })
  const refresh = () => {
    void cache.invalidateQueries({ queryKey: ['me'] })
    void cache.invalidateQueries({ queryKey: ['cloud-session'] })
    void cache.invalidateQueries({ queryKey: ['account-security'] })
    void cache.invalidateQueries({ queryKey: ['account-sessions'] })
  }
  const role = identity.owner ? 'Super admin' : identity.admin ? 'Administrator' : 'Member'
  const data = security.data
  const sessionCount = sessions.data?.items.length
  const summary = [
    data && (data.totp_enabled ? 'Two-factor on' : 'Two-factor off'),
    data && plural(data.passkeys.length, 'passkey'),
    sessionCount !== undefined && plural(sessionCount, 'active session'),
  ]
    .filter(Boolean)
    .join(' · ')
  const addPasskey = (
    <Button disabled={!data?.password_enabled} onClick={() => setAction('passkey')}>
      Add passkey
    </Button>
  )
  return (
    <>
      <SettingsSection
        title="Account security"
        help="How you sign in, and the browser and CLI sessions issued to your account."
        state={summary || undefined}
      >
        <SettingsRows label="Your account">
          <SettingsRow
            className="settings-row-member"
            mark={<Avatar name={identity.name || 'Member'} url={identity.avatar_url} />}
            name={identity.name || 'Member'}
            detail={identity.email}
            status={<span className="label-chip">{role}</span>}
            actions={
              <Button asChild size="sm">
                <Link to="/settings/profile">Edit profile</Link>
              </Button>
            }
          />
        </SettingsRows>
        {data?.organization_mfa_required && (
          <Note>
            MFA is required by your organization. Keep at least one authenticator or passkey.
          </Note>
        )}
        {data && !data.mfa_verified && data.totp_enabled && (
          <form
            className="settings-inline-controls"
            onSubmit={async (e) => {
              e.preventDefault()
              setBusy(true)
              if (busy) return
              setVerifyError('')
              try {
                await unwrap(client.POST('/auth/mfa/verify', { body: { code: verifyCode } }))
                setVerifyCode('')
                refresh()
              } catch (e) {
                setVerifyError(message(e))
              } finally {
                setBusy(false)
              }
            }}
          >
            <label className="grid gap-1.5">
              Verify this session
              <Input
                autoComplete="one-time-code"
                required
                value={verifyCode}
                onChange={(e) => setVerifyCode(e.target.value)}
                placeholder="Authenticator or recovery code"
              />
            </label>
            <Button type="submit" variant="primary" disabled={busy}>
              Verify MFA
            </Button>
            {verifyError && <RequestError error={verifyError} />}
          </form>
        )}
        {data && !data.mfa_verified && !data.totp_enabled && (
          <Note>
            Set up an authenticator below, or sign out and sign in with an existing passkey, to
            verify this session.
          </Note>
        )}
        {data && !data.password_enabled && (
          <Note>
            Set a password before adding an authenticator or passkey.{' '}
            <a href="/login/forgot">Set a password by email</a>. Existing passkeys still work for
            sign-in.
          </Note>
        )}
      </SettingsSection>
      {security.isPending ? (
        <Loading />
      ) : security.error ? (
        <ErrorState error={security.error} />
      ) : (
        data && (
          <>
            <SettingsSection
              level={3}
              title="Two-factor authentication"
              help="Protect sign-in with an authenticator app."
              state={
                data.totp_enabled
                  ? `On · ${plural(data.recovery_codes_remaining, 'recovery code')} left`
                  : 'Off'
              }
              actions={
                <Button
                  disabled={
                    !data.password_enabled ||
                    Boolean(
                      data.organization_mfa_required && data.totp_enabled && !data.passkeys.length,
                    )
                  }
                  onClick={() => setAction(data.totp_enabled ? 'disable' : 'totp')}
                >
                  {data.totp_enabled ? 'Disable authenticator' : 'Set up authenticator'}
                </Button>
              }
            />
            <SettingsSection
              level={3}
              title="Passkeys"
              help="Sign in with this device or a security key."
              state={data.passkeys.length ? plural(data.passkeys.length, 'passkey') : undefined}
              actions={data.passkeys.length > 0 && addPasskey}
            >
              {data.passkeys.length ? (
                <SettingsRows label="Passkeys">
                  {data.passkeys.map((key) => (
                    <SettingsRow
                      key={key.id}
                      mark={<Icon name="key" size={16} />}
                      name={key.name}
                      detail={`Added ${timestamp(key.created_at)}`}
                      meta={
                        key.last_used_at ? `Last used ${timestamp(key.last_used_at)}` : 'Never used'
                      }
                      actions={
                        <Button
                          size="sm"
                          variant="ghost"
                          disabled={!data.password_enabled}
                          aria-label={`Remove passkey ${key.name}`}
                          onClick={() => setRemove(key)}
                        >
                          Remove
                        </Button>
                      }
                    />
                  ))}
                </SettingsRows>
              ) : (
                <SettingsEmpty title="No passkeys" action={addPasskey}>
                  Add one to sign in without a password.
                </SettingsEmpty>
              )}
            </SettingsSection>
            {action && (
              <SecurityAction
                action={action}
                accountLabel={identity.email || identity.name || identity.id}
                mfa={data.totp_enabled}
                onClose={() => setAction(null)}
                onSuccess={refresh}
              />
            )}
            {remove && (
              <SecurityAction
                action="remove"
                accountLabel={identity.email || identity.name || identity.id}
                passkey={remove}
                mfa={data.totp_enabled}
                onClose={() => setRemove(null)}
                onSuccess={refresh}
              />
            )}
          </>
        )
      )}
      <SettingsSection
        level={3}
        title="Active sessions"
        help="Browser and CLI sessions issued to your account."
        state={sessionCount !== undefined ? plural(sessionCount, 'session') : undefined}
      >
        {sessions.isPending ? (
          <Loading rows={2} />
        ) : sessions.error ? (
          <ErrorState error={sessions.error} />
        ) : (
          <SettingsRows label="Active sessions">
            {sessions.data?.items.slice(0, 100).map((session) => (
              <SettingsRow
                key={session.id}
                mark={<Icon name={session.kind === 'browser' ? 'globe' : 'terminal'} size={16} />}
                name={<span className="capitalize">{session.kind}</span>}
                detail={`Created ${timestamp(session.created_at)} · Expires ${timestamp(session.expires_at)}`}
                status={session.current && <SettingsState tone="ok">This session</SettingsState>}
                meta={
                  session.last_used_at
                    ? `Last used ${timestamp(session.last_used_at)}`
                    : 'Never used'
                }
                actions={
                  <Button
                    size="sm"
                    variant="ghost"
                    onClick={() => {
                      setError('')
                      setRevoke(session)
                    }}
                    aria-label={`Revoke ${session.kind} session ${session.id}`}
                  >
                    Revoke
                  </Button>
                }
              />
            ))}
          </SettingsRows>
        )}
      </SettingsSection>
      <Dialog
        open={Boolean(revoke)}
        className={focusedDialog}
        onOpenChange={(open) => {
          if (!busy && !open) setRevoke(null)
        }}
        title="Revoke this session?"
        description={
          revoke?.current
            ? 'You will be signed out of this browser.'
            : 'This browser or terminal will need to sign in again.'
        }
      >
        <div className="dialog-body">
          {revoke && (
            <ReviewFacts
              facts={[
                ['Session', `${revoke.kind}${revoke.current ? ' · this session' : ''}`],
                ['Created', timestamp(revoke.created_at)],
                ['Last used', revoke.last_used_at ? timestamp(revoke.last_used_at) : 'Never used'],
              ]}
            />
          )}
          {error && <RequestError error={error} />}
        </div>
        <div className="dialog-footer">
          <Button disabled={busy} onClick={() => setRevoke(null)}>
            Cancel
          </Button>
          <Button
            variant="danger"
            disabled={busy || !revoke}
            onClick={async () => {
              if (!revoke || busy) return
              setBusy(true)
              setError('')
              try {
                await unwrap(
                  client.DELETE('/auth/sessions/{id}', { params: { path: { id: revoke.id } } }),
                )
                if (revoke.current) {
                  await fetch('/session', { method: 'DELETE' })
                  window.location.assign('/')
                } else {
                  setRevoke(null)
                  refresh()
                }
              } catch (err) {
                setError(message(err))
              } finally {
                setBusy(false)
              }
            }}
          >
            {busy ? 'Revoking…' : revoke?.current ? 'Revoke and sign out' : 'Revoke session'}
          </Button>
        </div>
      </Dialog>
    </>
  )
}

function SecurityAction({
  action,
  accountLabel,
  passkey,
  mfa,
  onClose,
  onSuccess,
}: {
  action: 'totp' | 'disable' | 'passkey' | 'remove'
  accountLabel: string
  passkey?: { id: string; name: string }
  mfa: boolean
  onClose: () => void
  onSuccess: () => void
}) {
  const [password, setPassword] = useState('')
  const [code, setCode] = useState('')
  const [name, setName] = useState('')
  const [confirmation, setConfirmation] = useState('')
  const confirmationTarget =
    action === 'remove'
      ? passkey?.name || passkey?.id
      : action === 'disable'
        ? accountLabel
        : undefined
  const [setup, setSetup] = useState<{
    challenge: string
    secret: string
    otpauth_url: string
  } | null>(null)
  const [recovery, setRecovery] = useState<string[]>([])
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const titles = {
    totp: 'Set up an authenticator',
    disable: 'Disable authenticator',
    passkey: 'Add a passkey',
    remove: `Remove ${passkey?.name || 'passkey'}`,
  }
  return (
    <Dialog
      open
      onOpenChange={(open) => {
        if (!busy && !open) onClose()
      }}
      title={titles[action]}
      description={
        recovery.length
          ? 'Save these recovery codes now. Each works once.'
          : 'Confirm this account change securely.'
      }
    >
      <form
        onSubmit={async (event) => {
          event.preventDefault()
          if (busy || (confirmationTarget && confirmation !== confirmationTarget)) return
          setBusy(true)
          setError('')
          try {
            if (action === 'totp' && !setup) {
              setSetup(await unwrap(client.POST('/auth/mfa/totp/start', { body: { password } })))
              setPassword('')
            } else if (action === 'totp' && setup) {
              const result = await unwrap(
                client.POST('/auth/mfa/totp/confirm', {
                  body: { challenge: setup.challenge, code },
                }),
              )
              setRecovery(result.recovery_codes)
              setSetup(null)
              setCode('')
              onSuccess()
            } else if (action === 'disable') {
              await unwrap(client.POST('/auth/mfa/totp/disable', { body: { password, code } }))
              onSuccess()
              onClose()
            } else if (action === 'remove' && passkey) {
              await unwrap(
                client.DELETE('/auth/passkeys/{id}', {
                  params: { path: { id: passkey.id } },
                  body: { password, code },
                }),
              )
              onSuccess()
              onClose()
            } else if (action === 'passkey') {
              const start = await unwrap(
                client.POST('/auth/passkeys/register/start', { body: { name, password, code } }),
              )
              setPassword('')
              setCode('')
              const credential = await passkeyCredential(start.options.publicKey, true)
              await unwrap(
                client.POST('/auth/passkeys/register/finish', {
                  body: { challenge: start.challenge, credential },
                }),
              )
              onSuccess()
              onClose()
            }
          } catch (err) {
            setError(message(err))
          } finally {
            setBusy(false)
          }
        }}
      >
        <div className="dialog-body auth-form">
          {recovery.length ? (
            <>
              <div className="recovery-codes">
                {recovery.map((value) => (
                  <code key={value}>{value}</code>
                ))}
              </div>
              <Copy value={recovery.join('\n')} />
              <Note>
                Keep these codes somewhere secure outside this account. They will not appear again.
              </Note>
            </>
          ) : setup ? (
            <>
              <p>
                Add this setup key to your authenticator app, then enter its current six-digit code.
              </p>
              <div className="secret-once">
                <code>{setup.secret}</code>
                <Copy value={setup.secret} />
              </div>
              <label>
                Authenticator code
                <Input
                  value={code}
                  onChange={(e) => setCode(e.target.value)}
                  autoComplete="one-time-code"
                  inputMode="numeric"
                  pattern="[0-9]{6}"
                  maxLength={6}
                  required
                />
              </label>
            </>
          ) : (
            <>
              {action === 'passkey' && (
                <label>
                  Passkey name
                  <Input
                    value={name}
                    onChange={(e) => setName(e.target.value)}
                    placeholder="This device"
                    maxLength={80}
                    required
                  />
                </label>
              )}
              <label>
                Current password
                <Input
                  type="password"
                  value={password}
                  onChange={(e) => setPassword(e.target.value)}
                  autoComplete="current-password"
                  maxLength={128}
                  required
                />
              </label>
              {(mfa || action === 'disable') && (
                <label>
                  Authenticator or recovery code
                  <Input
                    value={code}
                    onChange={(e) => setCode(e.target.value)}
                    autoComplete="one-time-code"
                    maxLength={128}
                    required
                  />
                </label>
              )}
              {confirmationTarget && (
                <label>
                  Type <code>{confirmationTarget}</code> to confirm{' '}
                  {action === 'remove' ? 'passkey removal' : 'disabling your authenticator'}
                  <Input
                    value={confirmation}
                    onChange={(event) => setConfirmation(event.target.value)}
                    autoComplete="off"
                    spellCheck={false}
                    required
                    aria-label={action === 'remove' ? 'Confirm passkey name' : 'Confirm account'}
                  />
                </label>
              )}
            </>
          )}
          {error && <RequestError error={error} />}
        </div>
        <div className="dialog-footer">
          <Button type="button" disabled={busy} onClick={onClose}>
            {recovery.length ? 'I saved my codes' : 'Cancel'}
          </Button>
          {!recovery.length && (
            <Button
              type="submit"
              variant={action === 'disable' || action === 'remove' ? 'danger' : 'primary'}
              disabled={busy || Boolean(confirmationTarget && confirmation !== confirmationTarget)}
            >
              {busy
                ? 'Working…'
                : setup
                  ? 'Verify authenticator'
                  : action === 'remove'
                    ? 'Remove passkey'
                    : action === 'disable'
                      ? 'Disable authenticator'
                      : action === 'passkey'
                        ? 'Register passkey'
                        : 'Start setup'}
            </Button>
          )}
        </div>
      </form>
    </Dialog>
  )
}
