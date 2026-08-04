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
import { Copy, ErrorState, Loading, Note, Status } from './shared'

export default function AccountSettings() {
  const { identity } = useScope()
  const cache = useQueryClient()
  const [action, setAction] = useState<'totp' | 'disable' | 'passkey' | null>(null)
  const [remove, setRemove] = useState<{ id: string; name: string } | null>(null)
  const [revoke, setRevoke] = useState<{ id: string; current: boolean } | null>(null)
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
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
    void cache.invalidateQueries({ queryKey: ['account-security'] })
    void cache.invalidateQueries({ queryKey: ['account-sessions'] })
  }
  return (
    <>
      <div className="section-toolbar">
        <div>
          <h2>Your account</h2>
          <p>
            {identity.name} · {identity.email}
          </p>
        </div>
        <div className="toolbar-actions">
          <Avatar name={identity.name || 'Member'} url={identity.avatar_url} />
          <Status
            value={identity.owner ? 'super admin' : identity.admin ? 'administrator' : 'member'}
          />
          <Link className="button button-sm" to="/settings/profile">
            Edit profile
          </Link>
        </div>
      </div>
      {security.isPending ? (
        <Loading />
      ) : security.error ? (
        <ErrorState error={security.error} />
      ) : (
        security.data && (
          <>
            {!security.data.password_enabled && (
              <Note>
                A password must be enrolled before changing authenticators or passkeys. Existing
                passkeys can still be used for sign-in.
              </Note>
            )}
            <div className="security-grid">
              <section className="panel service-summary-panel">
                <h2>Two-factor authentication</h2>
                <p className="muted-text">
                  {security.data.totp_enabled
                    ? `Enabled · ${security.data.recovery_codes_remaining} recovery codes remaining`
                    : 'Protect sign-in with an authenticator app.'}
                </p>
                <Button
                  disabled={!security.data.password_enabled}
                  onClick={() => setAction(security.data.totp_enabled ? 'disable' : 'totp')}
                >
                  {security.data.totp_enabled ? 'Disable authenticator' : 'Set up authenticator'}
                </Button>
              </section>
              <section className="panel service-summary-panel">
                <div className="section-toolbar">
                  <div>
                    <h2>Passkeys</h2>
                    <p>Use your device or a security key.</p>
                  </div>
                  <Button
                    disabled={!security.data.password_enabled}
                    onClick={() => setAction('passkey')}
                  >
                    Add passkey
                  </Button>
                </div>
                {security.data.passkeys.length ? (
                  security.data.passkeys.map((key) => (
                    <div className="settings-list-row" key={key.id}>
                      <div>
                        <strong>{key.name}</strong>
                        <small>
                          Added {timestamp(key.created_at)} · Last used{' '}
                          {timestamp(key.last_used_at)}
                        </small>
                      </div>
                      <Button
                        size="sm"
                        variant="ghost"
                        disabled={!security.data.password_enabled}
                        onClick={() => setRemove(key)}
                      >
                        Remove
                      </Button>
                    </div>
                  ))
                ) : (
                  <p className="field-help">No passkeys registered.</p>
                )}
              </section>
            </div>
            {action && (
              <SecurityAction
                action={action}
                mfa={security.data.totp_enabled}
                onClose={() => setAction(null)}
                onSuccess={refresh}
              />
            )}
            {remove && (
              <SecurityAction
                action="remove"
                passkey={remove}
                mfa={security.data.totp_enabled}
                onClose={() => setRemove(null)}
                onSuccess={refresh}
              />
            )}
          </>
        )
      )}
      <div className="section-toolbar">
        <div>
          <h2>Active sessions</h2>
          <p>Browser and CLI sessions issued to your account.</p>
        </div>
      </div>
      {sessions.isPending ? (
        <Loading rows={2} />
      ) : sessions.error ? (
        <ErrorState error={sessions.error} />
      ) : (
        <div className="panel settings-session-list">
          {sessions.data?.items.slice(0, 100).map((session) => (
            <div className="settings-list-row" key={session.id}>
              <div>
                <strong>
                  {session.kind}
                  {session.current ? ' · This session' : ''}
                </strong>
                <small>
                  Created {timestamp(session.created_at)} · Expires {timestamp(session.expires_at)}{' '}
                  · Last used {timestamp(session.last_used_at)}
                </small>
              </div>
              <Button
                size="sm"
                onClick={() => {
                  setError('')
                  setRevoke(session)
                }}
              >
                Revoke
              </Button>
            </div>
          ))}
        </div>
      )}
      <Dialog
        open={Boolean(revoke)}
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
          {error && (
            <div className="inline-error" role="alert">
              {error}
            </div>
          )}
        </div>
        <div className="dialog-footer">
          <Button disabled={busy} onClick={() => setRevoke(null)}>
            Cancel
          </Button>
          <Button
            variant="danger"
            disabled={busy}
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
            Revoke session
          </Button>
        </div>
      </Dialog>
    </>
  )
}

function SecurityAction({
  action,
  passkey,
  mfa,
  onClose,
  onSuccess,
}: {
  action: 'totp' | 'disable' | 'passkey' | 'remove'
  passkey?: { id: string; name: string }
  mfa: boolean
  onClose: () => void
  onSuccess: () => void
}) {
  const [password, setPassword] = useState('')
  const [code, setCode] = useState('')
  const [name, setName] = useState('')
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
          if (busy) return
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
                <input
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
                  <input
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
                <input
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
                  <input
                    value={code}
                    onChange={(e) => setCode(e.target.value)}
                    autoComplete="one-time-code"
                    maxLength={128}
                    required
                  />
                </label>
              )}
            </>
          )}
          {error && (
            <div className="inline-error" role="alert">
              {error}
            </div>
          )}
        </div>
        <div className="dialog-footer">
          <Button type="button" disabled={busy} onClick={onClose}>
            {recovery.length ? 'I saved my codes' : 'Cancel'}
          </Button>
          {!recovery.length && (
            <Button
              type="submit"
              variant={action === 'disable' || action === 'remove' ? 'danger' : 'primary'}
              disabled={busy}
            >
              {busy ? 'Working…' : setup ? 'Verify authenticator' : 'Continue'}
            </Button>
          )}
        </div>
      </form>
    </Dialog>
  )
}
