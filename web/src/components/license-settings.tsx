import { Textarea } from './ui/textarea'
import { useState } from 'react'
import { Link } from '@tanstack/react-router'
import { useQueryClient } from '@tanstack/react-query'
import { Badge, Card } from './ui/surfaces'
import { useLicense } from '../lib/license'
import { client, unwrap } from '../lib/client'
import { message, timestamp } from '../lib/api'
import { useScope } from '../lib/scope'
import { Button } from './ui/button'
import { Dialog } from './ui/dialog'
import { Icon } from './icons'
import { HeadingHelp, Copy, ErrorState, Loading, Note } from './shared'
export function FeatureLock({
  title = 'Team collaboration requires Hakopod Pro',
}: {
  title?: string
}) {
  return (
    <Card className="feature-lock">
      <div className="feature-lock-symbol">
        <Icon name="lock" size={23} />
      </div>
      <div>
        <div className="title-row">
          <h2>{title}</h2>
          <Badge tone="accent">PRO</Badge>
        </div>
        <p>
          Teams, invitations, and project role grants are licensed features. Existing access can
          still be removed.
        </p>
      </div>
      <Link to="/settings" search={{ tab: 'license' }} className="button button-secondary">
        View license
        <Icon name="arrow" size={14} />
      </Link>
    </Card>
  )
}
export default function LicenseSettings() {
  const scope = useScope()
  const license = useLicense()
  const cache = useQueryClient()
  const [value, setValue] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [remove, setRemove] = useState(false)
  if (license.isPending) return <Loading rows={3} />
  if (license.error || !license.data)
    return <ErrorState error={license.error} retry={() => void license.refetch()} />
  const current = license.data
  const refresh = async () => {
    await cache.invalidateQueries({ queryKey: ['license'] })
    await cache.invalidateQueries({ queryKey: ['me'] })
  }
  return (
    <>
      <div className="license-summary">
        <Card className="license-plan">
          <Icon name="shield" size={27} />
          <div className="title-row">
            <h2>Hakopod {current.plan === 'pro' ? 'Pro' : 'Free'}</h2>
            <Badge tone={current.valid ? 'success' : 'neutral'}>
              {current.state.replaceAll('_', ' ')}
            </Badge>
          </div>
          <p>
            {current.plan === 'pro'
              ? `Licensed to ${current.licensed_to || 'this installation'}`
              : 'Deploy and operate applications on your own infrastructure.'}
          </p>
          <dl>
            <div>
              <dt>Installation</dt>
              <dd>
                <code>{current.installation_id}</code>
                <Copy value={current.installation_id} />
              </dd>
            </div>
            {current.license_id && (
              <div>
                <dt>License</dt>
                <dd>{current.license_id}</dd>
              </div>
            )}
            <div>
              <dt>Expires</dt>
              <dd>{current.expires_at ? timestamp(current.expires_at) : 'No active expiry'}</dd>
            </div>
          </dl>
        </Card>
        <Card className="license-activation">
          <h2>{scope.identity.admin ? 'Activate a license' : 'Installation license'}</h2>
          {scope.identity.admin ? (
            <form
              onSubmit={async (e) => {
                e.preventDefault()
                if (busy) return
                setBusy(true)
                setError('')
                try {
                  await unwrap(
                    client.PUT('/license', {
                      body: { license: value.trim(), expected_revision: current.revision },
                    }),
                  )
                  setValue('')
                  await refresh()
                } catch (err) {
                  setError(message(err))
                } finally {
                  setBusy(false)
                }
              }}
            >
              <label>
                Signed license
                <Textarea
                  value={value}
                  onChange={(e) => setValue(e.target.value)}
                  rows={4}
                  maxLength={16384}
                  required
                  placeholder="Paste the license issued for this installation"
                  autoComplete="off"
                  spellCheck={false}
                />
              </label>
              <div className="toolbar-actions">
                <Button
                  type="submit"
                  variant="primary"
                  disabled={busy || !value.trim() || !current.issuer_configured}
                >
                  {busy ? 'Applying…' : 'Activate license'}
                </Button>
                {current.license_id && (
                  <Button
                    type="button"
                    variant="ghost"
                    disabled={busy}
                    onClick={() => setRemove(true)}
                  >
                    Remove license
                  </Button>
                )}
              </div>
              {!current.issuer_configured && (
                <p className="field-help">
                  This installation needs a trusted Hakopod release with its built-in license
                  verification key before a Pro license can be activated.
                </p>
              )}
            </form>
          ) : (
            <p className="muted-text">
              An installation administrator can activate or remove the license.
            </p>
          )}
        </Card>
      </div>
      {error && (
        <div className="inline-error" role="alert">
          {error}
        </div>
      )}
      <div className="section-toolbar">
        <div>
          <div className="hako-section-heading-title">
            <h2>Feature catalog</h2>
            <HeadingHelp title="Feature catalog">
              Availability is verified by the management API.
            </HeadingHelp>
          </div>
        </div>
        <Badge>{current.catalog.filter((feature) => feature.enabled).length} available</Badge>
      </div>
      <div className="license-feature-grid">
        {current.catalog.map((feature) => (
          <Card
            className={`license-feature ${feature.enabled ? '' : 'license-feature-locked'}`}
            key={feature.id}
          >
            <Icon name={feature.enabled ? 'check' : 'lock'} size={19} />
            <div>
              <h3>{feature.name}</h3>
              <p>{feature.description}</p>
            </div>
            <Badge tone={feature.plan === 'pro' ? 'accent' : 'neutral'}>
              {feature.plan.toUpperCase()}
            </Badge>
          </Card>
        ))}
      </div>
      <Note>
        Licensing is checked locally. Removing or expiring a license disables paid access grants;
        the installation administrator keeps recovery access.
      </Note>
      <Dialog
        open={remove}
        onOpenChange={(open) => {
          if (!busy) setRemove(open)
        }}
        title="Remove this license?"
        description="The installation returns to Free. Teams, invitations, and project role grants will stop authorizing paid actions."
      >
        <div className="dialog-body">
          <p>
            The highest license sequence is retained to prevent replay. To activate Pro again after
            removal, you need a newly issued license with a higher sequence; the same license cannot
            be reused.
          </p>
        </div>
        <div className="dialog-footer">
          <Button disabled={busy} onClick={() => setRemove(false)}>
            Keep license
          </Button>
          <Button
            variant="danger"
            disabled={busy}
            onClick={async () => {
              setBusy(true)
              setError('')
              try {
                await unwrap(
                  client.DELETE('/license', { body: { expected_revision: current.revision } }),
                )
                setRemove(false)
                await refresh()
              } catch (err) {
                setError(message(err))
              } finally {
                setBusy(false)
              }
            }}
          >
            Remove license
          </Button>
        </div>
      </Dialog>
    </>
  )
}
