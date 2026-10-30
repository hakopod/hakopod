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
import { Copy, ErrorState, Loading, RequestError } from './shared'
import { ReviewFacts } from './operation-review'
import { SettingsRow, SettingsRows, SettingsSection, SettingsState } from './settings-section'

// Opens the Pro checkout on hakopod.com with this installation's ID filled in.
const buyProURL = (installation: string) =>
  `https://hakopod.com/pricing/?installation=${encodeURIComponent(installation)}#buy-pro`
export function FeatureLock({
  title = 'This capability requires Hakopod Pro',
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
          Advanced capabilities require explicit signed entitlements. Teams, invitations and fixed
          project roles are included in Free.
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
  if (license.isPending || license.error || !license.data)
    return (
      <SettingsSection title="License">
        {license.isPending ? (
          <Loading rows={3} />
        ) : (
          <ErrorState error={license.error} retry={() => void license.refetch()} />
        )}
      </SettingsSection>
    )
  const current = license.data
  const refresh = async () => {
    await cache.invalidateQueries({ queryKey: ['license'] })
    await cache.invalidateQueries({ queryKey: ['me'] })
  }
  const available = current.catalog.filter((feature) => feature.enabled).length
  const plan = current.plan === 'pro' ? 'Pro' : 'Free'
  return (
    <>
      <SettingsSection
        title="License"
        help="Licensing is checked locally. Removing or expiring a license disables paid access grants; the installation administrator keeps recovery access."
        state={[
          `Hakopod ${plan}`,
          current.state !== current.plan && current.state.replaceAll('_', ' '),
          current.plan === 'pro' && `licensed to ${current.licensed_to || 'this installation'}`,
          current.expires_at ? `expires ${timestamp(current.expires_at)}` : 'no expiry',
        ]
          .filter(Boolean)
          .join(' · ')}
        actions={
          scope.identity.admin && (
            <>
              <Button asChild size="sm" variant={current.plan === 'pro' ? 'ghost' : 'primary'}>
                <a href={buyProURL(current.installation_id)} target="_blank" rel="noreferrer">
                  {current.plan === 'pro' ? 'Renew Pro' : 'Buy Pro'}
                </a>
              </Button>
              {current.license_id && (
                <Button variant="ghost" disabled={busy} onClick={() => setRemove(true)}>
                  <Icon name="trash" size={15} />
                  Remove license
                </Button>
              )}
            </>
          )
        }
      >
        <ReviewFacts
          facts={[
            [
              'Installation',
              <span className="copy-id" key="installation">
                <code>{current.installation_id}</code>
                <Copy value={current.installation_id} label="Copy installation ID" iconOnly />
              </span>,
            ],
            current.license_id && ['License', current.license_id],
          ]}
          label="License details"
        />
      </SettingsSection>
      <SettingsSection
        level={3}
        title={current.license_id ? 'Replace license' : 'Activate a license'}
        state={
          !scope.identity.admin
            ? 'An installation administrator can activate or remove the license.'
            : !current.issuer_configured
              ? 'This installation needs a trusted Hakopod release with its built-in license verification key before a Pro license can be activated.'
              : undefined
        }
      >
        {scope.identity.admin && (
          <form
            className="grid gap-3"
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
            <label className="grid gap-1.5">
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
            <div>
              <Button
                type="submit"
                variant="primary"
                disabled={busy || !value.trim() || !current.issuer_configured}
              >
                {busy ? 'Applying…' : 'Activate license'}
              </Button>
            </div>
          </form>
        )}
        {error && <RequestError error={error} />}
      </SettingsSection>
      <SettingsSection
        level={3}
        title="Features"
        help="Availability is verified by the management API."
        state={`${available} of ${current.catalog.length} available`}
      >
        <SettingsRows label="Features">
          {current.catalog.map((feature) => (
            <SettingsRow
              key={feature.id}
              className="settings-row-feature"
              muted={!feature.enabled}
              mark={<Icon name={feature.enabled ? 'check' : 'lock'} size={16} />}
              name={feature.name}
              detail={feature.description}
              status={
                <SettingsState tone={feature.enabled ? 'ok' : 'muted'}>
                  {feature.enabled ? 'Available' : 'Locked'}
                </SettingsState>
              }
              meta={feature.plan === 'pro' ? 'Pro' : 'Free'}
            />
          ))}
        </SettingsRows>
      </SettingsSection>
      <Dialog
        open={remove}
        onOpenChange={(open) => {
          if (!busy) setRemove(open)
        }}
        title="Remove this license?"
        description="The installation returns to Free. Paid capabilities stop authorizing actions. Teams, invitations and fixed project roles remain available."
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
