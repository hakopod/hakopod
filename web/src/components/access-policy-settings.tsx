import { useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { client, unwrap } from '../lib/client'
import { APIError, message } from '../lib/api'
import type { components } from '../lib/api.generated'
import { InstallationAccess } from './installation-access'
import { Button } from './ui/button'
import { Field } from './ui/field'
import { SelectField } from './ui/select'
import { Textarea } from './ui/textarea'
import { ErrorState, Loading, Note, RequestError } from './shared'
import { SettingsSection } from './settings-section'
import { GuidedEmpty } from './resource-card'
import { VPNProfiles } from './vpn-profiles'

type Access = components['schemas']['InstallationAccess']
type Policy = components['schemas']['InstallationAccessPolicy']
type VPNStatus = components['schemas']['InstallationAccessVPNStatus']

const lines = (value: string) =>
  value
    .split(/[\s,]+/)
    .map((item) => item.trim())
    .filter(Boolean)

function useAccessPolicy() {
  return useQuery({
    queryKey: ['installation-access'],
    queryFn: ({ signal }) => unwrap(client.GET('/installation/access', { signal })),
    refetchInterval: (query) => (query.state.data?.pending_until ? 5000 : false),
    refetchOnWindowFocus: false,
    retry: false,
  })
}

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

// The VPN server takes the first address of its IPv4 network.
function firstAddress(network: string) {
  const [base] = network.split('/')
  const value = base.split('.').reduce((total, part) => total * 256 + Number(part), 0) + 1
  return [24, 16, 8, 0].map((shift) => Math.floor(value / 2 ** shift) % 256).join('.')
}

function vpnState(status?: VPNStatus) {
  if (!status) return 'Not connected'
  if (status.error) return `Error · ${status.error}`
  if (!status.latest_handshake) return 'Waiting for the first handshake'
  const seconds = Math.round((Date.now() - new Date(status.latest_handshake).getTime()) / 1000)
  return seconds < 180
    ? `Connected · last handshake ${seconds}s ago`
    : `Stale · last handshake ${Math.round(seconds / 60)} min ago`
}

function AccessPolicyForm({ current }: { current: Access }) {
  const cache = useQueryClient()
  const policy = current.policy
  const [enabled, setEnabled] = useState(policy.enabled)
  const [allow, setAllow] = useState(policy.allow_cidrs.join('\n'))
  const [source, setSource] = useState<Policy['client_ip_source']>(
    policy.client_ip_source || 'connection',
  )
  const [proxies, setProxies] = useState(policy.trusted_proxy_cidrs.join('\n'))
  const [header, setHeader] = useState<Policy['client_ip_header']>(policy.client_ip_header || '')
  const [firewall, setFirewall] = useState(Boolean(policy.firewall))
  const [vpnOn, setVPNOn] = useState(Boolean(policy.vpn))
  const [vpnAddress, setVPNAddress] = useState(policy.vpn?.address || '')
  const [vpnPeer, setVPNPeer] = useState(policy.vpn?.peer_public_key || '')
  const [vpnEndpoint, setVPNEndpoint] = useState(policy.vpn?.peer_endpoint || '')
  const [vpnAllowed, setVPNAllowed] = useState((policy.vpn?.allowed_ips || []).join('\n'))
  const [vpnOnly, setVPNOnly] = useState(Boolean(policy.vpn_only))
  const [serverOn, setServerOn] = useState(Boolean(policy.vpn_server))
  const [serverNetwork, setServerNetwork] = useState(policy.vpn_server?.network || '10.100.0.0/24')
  const [serverPort, setServerPort] = useState(String(policy.vpn_server?.listen_port || 51820))
  const [serverEndpoint, setServerEndpoint] = useState(policy.vpn_server?.endpoint || '')
  const [elsewhere, setElsewhere] = useState(false)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const pending = current.pending_until ? new Date(current.pending_until) : null

  async function run(action: () => Promise<Access>) {
    if (busy) return
    setBusy(true)
    setError('')
    try {
      cache.setQueryData(['installation-access'], await action())
    } catch (cause) {
      setError(message(cause))
      if (cause instanceof APIError && cause.status === 409)
        void cache.invalidateQueries({ queryKey: ['installation-access'] })
    } finally {
      setBusy(false)
    }
  }
  const save = () =>
    run(() =>
      unwrap(
        client.PUT('/installation/access', {
          body: {
            expected_revision: current.revision,
            confirm_from_elsewhere: elsewhere,
            policy: {
              enabled,
              allow_cidrs: lines(allow),
              client_ip_source: source,
              trusted_proxy_cidrs: source === 'trusted_proxy' ? lines(proxies) : [],
              client_ip_header: source === 'trusted_proxy' ? header : '',
              firewall: enabled && firewall,
              vpn_only: enabled && (vpnOn || serverOn) && vpnOnly,
              ...(serverOn
                ? {
                    vpn_server: {
                      network: serverNetwork.trim(),
                      listen_port: Number(serverPort) || 0,
                      endpoint: serverEndpoint.trim(),
                    },
                  }
                : {}),
              ...(vpnOn
                ? {
                    vpn: {
                      address: vpnAddress.trim(),
                      peer_public_key: vpnPeer.trim(),
                      peer_endpoint: vpnEndpoint.trim(),
                      allowed_ips: lines(vpnAllowed),
                    },
                  }
                : {}),
            },
          },
        }),
      ),
    )
  const confirm = () =>
    run(() =>
      unwrap(client.POST('/installation/access/confirm', { body: { revision: current.revision } })),
    )

  const state = pending
    ? `Waiting for confirmation · reverts at ${pending.toLocaleTimeString()}`
    : policy.enabled
      ? `Restricted · ${policy.allow_cidrs.length} allowed ${policy.allow_cidrs.length === 1 ? 'network' : 'networks'} · you are ${current.client_ip || 'unknown'}`
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
            onClick={() => void confirm()}
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
          onValueChange={(value) => setSource(value as Policy['client_ip_source'])}
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
              onValueChange={(value) => setHeader(value as Policy['client_ip_header'])}
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
        <p className="field-help m-0">
          Needs the private-access installer module on the server. Cluster nodes and the cluster
          network stay allowed; add the networks of any new agent nodes before they join.
        </p>
        <SettingsSection
          level={3}
          title="WireGuard connector"
          help="Connects this server to your WireGuard network as a client. The server's private key is generated on the server and never leaves it; add the public key below as a peer on your VPN. Only the networks you list are routed through the tunnel. Needs the private-access installer module."
          state={vpnState(policy.vpn ? current.vpn_status : undefined)}
        >
          <label className="flex items-center gap-2 text-sm">
            <input
              type="checkbox"
              checked={vpnOn}
              onChange={(event) => setVPNOn(event.target.checked)}
            />
            Connect this server to a WireGuard network
          </label>
          {current.vpn_status?.public_key && (
            <Field
              label="Server public key"
              readOnly
              value={current.vpn_status.public_key}
              help="Add this key as a peer on your WireGuard server."
            />
          )}
          {vpnOn && (
            <>
              <Field
                label="Server tunnel address"
                value={vpnAddress}
                placeholder="For example 10.99.0.2/32"
                onChange={(event) => setVPNAddress(event.target.value)}
              />
              <Field
                label="Peer public key"
                value={vpnPeer}
                onChange={(event) => setVPNPeer(event.target.value)}
              />
              <Field
                label="Peer endpoint"
                value={vpnEndpoint}
                placeholder="For example vpn.example.com:51820"
                onChange={(event) => setVPNEndpoint(event.target.value)}
              />
              <Field
                label="Networks routed through the tunnel"
                value={vpnAllowed}
                placeholder="For example 10.99.0.0/24"
                onChange={(event) => setVPNAllowed(event.target.value)}
                help="Space or comma separated. Add the same networks to Allowed networks to admit people on the VPN."
              />
              <label className="flex items-center gap-2 text-sm">
                <input
                  type="checkbox"
                  checked={vpnOnly}
                  disabled={!enabled}
                  onChange={(event) => setVPNOnly(event.target.checked)}
                />
                Admit only people on the VPN
              </label>
              <p className="field-help m-0">
                Every allowed network must be inside a routed network. It turns on only after a
                handshake in the last two minutes. With the server firewall, the Kubernetes API is
                then reachable only over the tunnel; the dashboard port stays open for signed
                webhooks and refuses everything else.
              </p>
            </>
          )}
        </SettingsSection>
        <SettingsSection
          level={3}
          title="Hakopod VPN server"
          help="Runs a WireGuard server on this host for administrators, so you do not need a VPN of your own. Open the UDP port in any cloud or network firewall in front of the server. Needs the private-access installer module."
          state={
            !policy.vpn_server
              ? 'Off'
              : current.vpn_status?.server_error
                ? `Error · ${current.vpn_status.server_error}`
                : current.vpn_status?.server_public_key
                  ? `Running · ${policy.vpn_server.endpoint}`
                  : 'Starting · waiting for the server to report its key'
          }
        >
          <label className="flex items-center gap-2 text-sm">
            <input
              type="checkbox"
              checked={serverOn}
              onChange={(event) => setServerOn(event.target.checked)}
            />
            Run a WireGuard server on this host
          </label>
          {serverOn && (
            <>
              <Field
                label="Public endpoint"
                value={serverEndpoint}
                placeholder="For example vpn.example.com:51820"
                onChange={(event) => setServerEndpoint(event.target.value)}
                help="The host name or address and UDP port that devices dial."
              />
              <Field
                label="UDP listen port"
                inputMode="numeric"
                value={serverPort}
                onChange={(event) => setServerPort(event.target.value)}
              />
              <Field
                label="VPN network"
                value={serverNetwork}
                onChange={(event) => setServerNetwork(event.target.value)}
                help="Devices get addresses here; the server takes the first one. Add this network to Allowed networks to admit them."
              />
            </>
          )}
        </SettingsSection>
        <label className="flex items-center gap-2 text-sm">
          <input
            type="checkbox"
            checked={elsewhere}
            onChange={(event) => setElsewhere(event.target.checked)}
          />
          Save even if it blocks my current network; I will confirm from an allowed one
        </label>
        <p className="field-help m-0">
          A saved policy reverts after five minutes unless you confirm it. If you are locked out,
          run <code>hakopod-server access-reset</code> on the server with the API environment; the
          Management network access guide has the exact command.
        </p>
        {error && <RequestError error={error} />}
        <div>
          <Button type="submit" size="sm" variant="primary" disabled={busy}>
            {busy ? 'Saving…' : 'Save policy'}
          </Button>
        </div>
      </form>
      {policy.vpn_server && <VPNProfiles serverAddress={firstAddress(policy.vpn_server.network)} />}
    </SettingsSection>
  )
}
