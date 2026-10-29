import { useState } from 'react'
import { Link } from '@tanstack/react-router'
import { useQueryClient } from '@tanstack/react-query'
import { APIError, message } from '../lib/api'
import {
  accessQueryKey,
  confirmPolicy,
  lines,
  savePolicy,
  useAccessPolicy,
  type Access,
  type AccessPolicy,
} from '../lib/access-policy'
import { InstallationAccess } from './installation-access'
import { Button } from './ui/button'
import { Field } from './ui/field'
import { SelectField } from './ui/select'
import { Textarea } from './ui/textarea'
import { ErrorState, Loading, Note, RequestError } from './shared'
import { SettingsSection } from './settings-section'
import { GuidedEmpty } from './resource-card'

export function AccessPolicyPanel() {
  return (
    <InstallationAccess managedDescription="Management network access is run by the Cloud service.">
      <AccessPolicyLoader />
    </InstallationAccess>
  )
}

const help =
  'Only the networks you list can reach the dashboard, the API, the CLI and MCP. Signed GitHub and GitLab webhooks stay reachable, and SSH to the server always works. A saved policy reverts after five minutes unless you confirm it from a network it allows.'

function AccessPolicyLoader() {
  const query = useAccessPolicy()
  if (query.isPending || query.error || !query.data)
    return (
      <SettingsSection title="Network access" help={help}>
        {query.isPending ? (
          <Loading />
        ) : (
          <ErrorState error={query.error} retry={() => void query.refetch()} />
        )}
      </SettingsSection>
    )
  return <AccessPolicyForm key={query.data.revision} current={query.data} />
}

function AccessPolicyForm({ current }: { current: Access }) {
  const cache = useQueryClient()
  const policy = current.policy
  const vpnAvailable = Boolean(policy.vpn || policy.vpn_server)
  const [enabled, setEnabled] = useState(policy.enabled)
  const [allow, setAllow] = useState(policy.allow_cidrs.join('\n'))
  const [source, setSource] = useState<AccessPolicy['client_ip_source']>(
    policy.client_ip_source || 'connection',
  )
  const [proxies, setProxies] = useState(policy.trusted_proxy_cidrs.join('\n'))
  const [header, setHeader] = useState<AccessPolicy['client_ip_header']>(
    policy.client_ip_header || '',
  )
  const [firewall, setFirewall] = useState(Boolean(policy.firewall))
  const [vpnOnly, setVPNOnly] = useState(Boolean(policy.vpn_only))
  const [elsewhere, setElsewhere] = useState(false)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const pending = current.pending_until ? new Date(current.pending_until) : null

  async function run(action: () => Promise<Access>) {
    if (busy) return
    setBusy(true)
    setError('')
    try {
      cache.setQueryData(accessQueryKey, await action())
    } catch (cause) {
      setError(message(cause))
      if (cause instanceof APIError && cause.status === 409)
        void cache.invalidateQueries({ queryKey: accessQueryKey })
    } finally {
      setBusy(false)
    }
  }
  const save = () =>
    run(() =>
      savePolicy(
        current,
        {
          enabled,
          allow_cidrs: lines(allow),
          client_ip_source: source,
          trusted_proxy_cidrs: source === 'trusted_proxy' ? lines(proxies) : [],
          client_ip_header: source === 'trusted_proxy' ? header : '',
          firewall: enabled && firewall,
          vpn_only: enabled && vpnAvailable && vpnOnly,
        },
        { confirmFromElsewhere: elsewhere },
      ),
    )

  const state = pending
    ? `Waiting for confirmation · reverts at ${pending.toLocaleTimeString()}`
    : policy.enabled
      ? `Restricted · ${policy.allow_cidrs.length} allowed ${policy.allow_cidrs.length === 1 ? 'network' : 'networks'}${policy.vpn_only ? ' · VPN only' : ''} · you are ${current.client_ip || 'unknown'}`
      : `Off · every network can reach the dashboard · you are ${current.client_ip || 'unknown'}`
  return (
    <SettingsSection
      title="Network access"
      help={help}
      state={state}
      actions={
        pending && (
          <Button
            variant="primary"
            disabled={busy}
            aria-busy={busy || undefined}
            onClick={() => void run(() => confirmPolicy(current))}
          >
            {busy ? 'Confirming…' : 'Keep this policy'}
          </Button>
        )
      }
    >
      {!current.licensed && (
        <Note>Network access restrictions need a Pro license. You can still turn them off.</Note>
      )}
      {pending && (
        <Note>
          Reaching this page through the new policy is the proof that it does not lock you out.
          Without confirmation the previous policy comes back at {pending.toLocaleTimeString()}.
        </Note>
      )}
      {!policy.enabled && !enabled && (
        <GuidedEmpty
          compact
          icon="lock"
          title="Keep the dashboard off the public internet"
          description="List your office, VPN or bastion networks. Every other network is refused before sign-in."
        />
      )}
      <form
        className="grid gap-4"
        aria-label="Network access policy"
        onSubmit={(event) => {
          event.preventDefault()
          void save()
        }}
      >
        <label className="flex items-center gap-2 text-sm">
          <input
            type="checkbox"
            checked={enabled}
            onChange={(event) => setEnabled(event.target.checked)}
          />
          Restrict management access to the networks below
        </label>
        <div className="field">
          <div className="field-heading">
            <label htmlFor="access-allow">Allowed networks</label>
          </div>
          <Textarea
            id="access-allow"
            rows={5}
            value={allow}
            placeholder="For example 10.8.0.0/24"
            onChange={(event) => setAllow(event.target.value)}
          />
          <div className="field-help">
            One CIDR per line, IPv4 or IPv6.
            {current.client_ip && (
              <>
                {' '}
                <Button
                  type="button"
                  size="sm"
                  variant="outline"
                  onClick={() =>
                    setAllow((old) =>
                      [
                        ...lines(old),
                        `${current.client_ip}/${current.client_ip.includes(':') ? 128 : 32}`,
                      ].join('\n'),
                    )
                  }
                >
                  Add {current.client_ip}
                </Button>
              </>
            )}
          </div>
        </div>
        <SelectField
          label="Client address"
          value={source}
          onValueChange={(value) => setSource(value as AccessPolicy['client_ip_source'])}
          options={[
            { value: 'connection', label: 'Connection address' },
            { value: 'trusted_proxy', label: 'Header from a trusted proxy or load balancer' },
          ]}
        />
        {source === 'trusted_proxy' && (
          <>
            <SelectField
              label="Client address header"
              value={header}
              onValueChange={(value) => setHeader(value as AccessPolicy['client_ip_header'])}
              options={[
                { value: 'X-Forwarded-For', label: 'X-Forwarded-For' },
                { value: 'X-Real-IP', label: 'X-Real-IP' },
                { value: 'CF-Connecting-IP', label: 'CF-Connecting-IP' },
              ]}
            />
            <Field
              label="Trusted proxy networks"
              value={proxies}
              onChange={(event) => setProxies(event.target.value)}
              help="The header is read only from these addresses, space or comma separated."
            />
          </>
        )}
        <label className="flex items-center gap-2 text-sm">
          <input
            type="checkbox"
            checked={firewall}
            disabled={!enabled}
            onChange={(event) => setFirewall(event.target.checked)}
          />
          Also close the dashboard port and the Kubernetes API (6443) in the server firewall
        </label>
        <label className="flex items-center gap-2 text-sm">
          <input
            type="checkbox"
            checked={vpnOnly}
            disabled={!enabled || !vpnAvailable}
            onChange={(event) => setVPNOnly(event.target.checked)}
          />
          Admit only people on the VPN
        </label>
        <p className="field-help m-0">
          The firewall and the VPN run in the hakopod-vpn agent; see{' '}
          <Link to="/settings" search={{ tab: 'vpn' }}>
            VPN
          </Link>
          . VPN only needs a WireGuard server or connector there, and every allowed network inside
          it.
        </p>
        <label className="flex items-center gap-2 text-sm">
          <input
            type="checkbox"
            checked={elsewhere}
            onChange={(event) => setElsewhere(event.target.checked)}
          />
          Save even if it blocks my current network; I will confirm from an allowed one
        </label>
        <p className="field-help m-0">
          If you are locked out, run <code>hakopod-server access-reset</code> on the server with the
          API environment; the Management network access guide has the exact command.
        </p>
        {error && <RequestError error={error} />}
        <div>
          <Button type="submit" variant="primary" disabled={busy}>
            {busy ? 'Saving…' : 'Save policy'}
          </Button>
        </div>
      </form>
    </SettingsSection>
  )
}
