import { useState, type ReactNode } from 'react'
import { Link, useNavigate } from '@tanstack/react-router'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { client, unwrap } from '../lib/client'
import { APIError, message } from '../lib/api'
import {
  accessQueryKey,
  agentState,
  ago,
  confirmPolicy,
  firstAddress,
  installCommand,
  lines,
  savePolicy,
  useAccessPolicy,
  type Access,
  type AccessPolicy,
  type AccessSecrets,
} from '../lib/access-policy'
import type { SecretAction } from '../lib/installation-settings'
import { wireguardKeyPair, wireguardProfile } from '../lib/wireguard'
import { FormPage, FormSection } from './form-page'
import { InstallationAccess } from './installation-access'
import {
  InstallationCredential,
  InstallationReviewRows,
  useInstallationFormFocus,
} from './installation-form-fields'
import { GuidedEmpty } from './resource-card'
import { SettingsRow, SettingsRows, SettingsSection, SettingsState } from './settings-section'
import { Copy, ErrorState, Loading, Note, RequestError } from './shared'
import { Button } from './ui/button'
import { Input } from './ui/input'
import { SelectField } from './ui/select'
import { Textarea } from './ui/textarea'

const toVPN = { to: '/settings', search: { tab: 'vpn' } } as const

function useAccess(children: (current: Access) => ReactNode, title: string) {
  const query = useAccessPolicy()
  if (query.isPending || query.error || !query.data)
    return (
      <SettingsSection title={title}>
        {query.isPending ? (
          <Loading />
        ) : (
          <ErrorState error={query.error} retry={() => void query.refetch()} />
        )}
      </SettingsSection>
    )
  return children(query.data)
}

// ---------------------------------------------------------------- Overview

export function VPNPanel() {
  return (
    <InstallationAccess managedDescription="The management VPN is run by the Cloud service.">
      <VPNLoader />
    </InstallationAccess>
  )
}

function VPNLoader() {
  return useAccess((current) => <VPNOverview current={current} />, 'VPN')
}

function VPNOverview({ current }: { current: Access }) {
  const agent = agentState(current)
  return (
    <div className="grid gap-[var(--section-gap)]">
      {agent !== 'running' && <AgentMissing current={current} stale={agent === 'stale'} />}
      <PendingConfirmation current={current} />
      <ServerSection current={current} />
      {current.policy.vpn_server && <DevicesSection current={current} />}
      <ConnectorSection current={current} />
    </div>
  )
}

function AgentMissing({ current, stale }: { current: Access; stale: boolean }) {
  return (
    <SettingsSection
      title="VPN agent"
      help="The VPN, its devices, connectors and the server firewall run in hakopod-vpn, a separate agent that is downloaded from this release only when you install it."
      state={
        stale ? (
          <SettingsState tone="warn">
            Not reporting · last seen {ago(current.agent?.seen_at)} · version{' '}
            {current.agent?.version}
          </SettingsState>
        ) : (
          <SettingsState tone="muted">Not installed on this server</SettingsState>
        )
      }
    >
      <div className="flex min-w-0 items-center gap-3">
        <code className="break-all">{installCommand}</code>
        <Copy value={installCommand} label="Copy command" />
      </div>
      <p className="field-help m-0">
        {stale
          ? 'Check the agent on the server with systemctl status hakopod-access-firewall.timer. Saved settings apply once it reports again.'
          : 'Run it from the installer kit of this release on the server. You can set up the VPN below now; it starts within ten seconds of the install.'}
      </p>
    </SettingsSection>
  )
}

function PendingConfirmation({ current }: { current: Access }) {
  const cache = useQueryClient()
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  if (!current.pending_until) return null
  const until = new Date(current.pending_until).toLocaleTimeString()
  return (
    <SettingsSection
      title="Confirm the new access policy"
      state={<SettingsState tone="warn">Reverts at {until} unless confirmed</SettingsState>}
      actions={
        <Button
          variant="primary"
          disabled={busy}
          aria-busy={busy || undefined}
          onClick={async () => {
            setBusy(true)
            setError('')
            try {
              cache.setQueryData(accessQueryKey, await confirmPolicy(current))
            } catch (cause) {
              setError(message(cause))
            } finally {
              setBusy(false)
            }
          }}
        >
          {busy ? 'Confirming…' : 'Keep this policy'}
        </Button>
      }
    >
      <p className="field-help m-0">
        Reaching this page through the new policy shows it does not lock you out.
      </p>
      {error && <RequestError error={error} />}
    </SettingsSection>
  )
}

function ServerSection({ current }: { current: Access }) {
  const server = current.policy.vpn_server
  const status = current.vpn_status
  const action = (
    <Button variant={server ? 'outline' : 'primary'} asChild>
      <Link to="/settings/vpn/setup">{server ? 'Edit' : 'Set up'}</Link>
    </Button>
  )
  return (
    <SettingsSection
      title="Hakopod VPN server"
      help="A WireGuard server on this host for administrators. Each device gets its own key and address; the dashboard and the Kubernetes API are reached at the server's VPN address."
      state={
        !server ? (
          <SettingsState tone="muted">Off</SettingsState>
        ) : status?.server_error ? (
          <SettingsState tone="danger">Error · {status.server_error}</SettingsState>
        ) : status?.server_public_key ? (
          <SettingsState tone="ok">Running · {server.endpoint}</SettingsState>
        ) : (
          <SettingsState tone="warn">Starting · waiting for the agent</SettingsState>
        )
      }
      actions={server && action}
    >
      {server ? (
        <InstallationReviewRows
          rows={[
            ['Endpoint', server.endpoint],
            ['VPN network', server.network],
            ['Dashboard on the VPN', firstAddress(server.network)],
            [
              'Server public key',
              <code key="key">{status?.server_public_key || 'Not reported yet'}</code>,
            ],
          ]}
        />
      ) : (
        <GuidedEmpty
          compact
          icon="lock"
          title="Run a VPN for administrators"
          description="No VPN of your own needed. Devices connect to this server and reach the dashboard privately."
          primary={action}
        />
      )}
    </SettingsSection>
  )
}

function DevicesSection({ current }: { current: Access }) {
  const cache = useQueryClient()
  const query = useQuery({
    queryKey: ['installation-vpn-peers'],
    queryFn: ({ signal }) => unwrap(client.GET('/installation/access/vpn-peers', { signal })),
    refetchInterval: 15000,
    retry: false,
  })
  const [confirming, setConfirming] = useState('')
  const [error, setError] = useState('')
  const peers = query.data?.items || []
  const connected = peers.filter(
    (peer) =>
      peer.latest_handshake && Date.now() - new Date(peer.latest_handshake).getTime() < 180_000,
  ).length
  const add = (
    <Button variant={peers.length ? 'outline' : 'primary'} asChild>
      <Link to="/settings/vpn/devices/new">Add device</Link>
    </Button>
  )
  async function revoke(id: string) {
    setError('')
    try {
      await unwrap(
        client.DELETE('/installation/access/vpn-peers/{id}', { params: { path: { id } } }),
      )
      setConfirming('')
      void cache.invalidateQueries({ queryKey: ['installation-vpn-peers'] })
    } catch (cause) {
      setError(message(cause))
    }
  }
  return (
    <SettingsSection
      title="Devices"
      help="One profile per device. Revoking a device disconnects it within ten seconds."
      state={
        query.isPending
          ? undefined
          : `${peers.length} ${peers.length === 1 ? 'device' : 'devices'} · ${connected} connected`
      }
      actions={peers.length > 0 && add}
    >
      {query.isPending ? (
        <Loading />
      ) : query.error ? (
        <ErrorState error={query.error} retry={() => void query.refetch()} />
      ) : peers.length === 0 ? (
        <GuidedEmpty
          compact
          icon="lock"
          title="Add your first device"
          description="Create a profile for a laptop or phone and import it into WireGuard."
          primary={add}
        />
      ) : (
        <SettingsRows label="VPN devices">
          {peers.map((peer) => {
            const live =
              peer.latest_handshake &&
              Date.now() - new Date(peer.latest_handshake).getTime() < 180_000
            return (
              <SettingsRow
                key={peer.id}
                name={peer.name}
                detail={`${peer.address} · added ${ago(peer.created_at)}`}
                status={
                  live ? (
                    <SettingsState tone="ok">Connected {ago(peer.latest_handshake)}</SettingsState>
                  ) : (
                    <SettingsState tone="muted">
                      {peer.latest_handshake
                        ? `Last seen ${ago(peer.latest_handshake)}`
                        : 'Never connected'}
                    </SettingsState>
                  )
                }
                actions={
                  confirming === peer.id ? (
                    <>
                      <Button size="sm" variant="outline" onClick={() => setConfirming('')}>
                        Cancel
                      </Button>
                      <Button size="sm" variant="danger" onClick={() => void revoke(peer.id)}>
                        Revoke {peer.name}
                      </Button>
                    </>
                  ) : (
                    <Button size="sm" variant="outline" onClick={() => setConfirming(peer.id)}>
                      Revoke
                    </Button>
                  )
                }
              />
            )
          })}
        </SettingsRows>
      )}
      {error && <RequestError error={error} />}
      {current.policy.vpn_server && peers.length > 0 && (
        <p className="field-help m-0">
          On devices, point this dashboard's host name at{' '}
          {firstAddress(current.policy.vpn_server.network)}.
        </p>
      )}
    </SettingsSection>
  )
}

const connectorNames = {
  wireguard: 'WireGuard',
  tailscale: 'Tailscale',
  openvpn: 'OpenVPN',
  ipsec: 'IPsec',
} as const
type ConnectorType = keyof typeof connectorNames

function connectorOf(policy: AccessPolicy): ConnectorType | undefined {
  if (policy.vpn) return 'wireguard'
  if (policy.tailscale) return 'tailscale'
  if (policy.openvpn) return 'openvpn'
  if (policy.ipsec) return 'ipsec'
  return undefined
}

function ConnectorSection({ current }: { current: Access }) {
  const policy = current.policy
  const type = connectorOf(policy)
  const status = current.connector_status
  const wireguard = current.vpn_status
  const action = (
    <Button variant={type ? 'outline' : 'primary'} asChild>
      <Link to="/settings/vpn/connector">{type ? 'Edit' : 'Connect'}</Link>
    </Button>
  )
  const state = !type ? (
    <SettingsState tone="muted">Not connected</SettingsState>
  ) : type === 'wireguard' ? (
    wireguard?.error ? (
      <SettingsState tone="danger">Error · {wireguard.error}</SettingsState>
    ) : wireguard?.latest_handshake &&
      Date.now() - new Date(wireguard.latest_handshake).getTime() < 180_000 ? (
      <SettingsState tone="ok">
        Connected · handshake {ago(wireguard.latest_handshake)}
      </SettingsState>
    ) : (
      <SettingsState tone="warn">Waiting for a handshake</SettingsState>
    )
  ) : status?.error ? (
    <SettingsState tone="danger">Error · {status.error}</SettingsState>
  ) : status?.connected ? (
    <SettingsState tone="ok">
      Connected · {connectorNames[type]}
      {status.address ? ` · ${status.address}` : ''}
    </SettingsState>
  ) : (
    <SettingsState tone="warn">Connecting</SettingsState>
  )
  const rows: [string, ReactNode][] = !type
    ? []
    : type === 'wireguard'
      ? [
          ['Type', 'WireGuard'],
          ['Peer', policy.vpn!.peer_endpoint],
          ['Routed networks', policy.vpn!.allowed_ips.join(', ')],
          ['Server public key', <code key="k">{wireguard?.public_key || 'Not reported yet'}</code>],
        ]
      : type === 'tailscale'
        ? [
            ['Type', policy.tailscale!.login_server ? 'Headscale' : 'Tailscale'],
            ['Hostname', policy.tailscale!.hostname || 'hakopod'],
            ['Auth key', current.connector_secrets_set?.tailscale_auth_key ? 'Stored' : 'Missing'],
          ]
        : type === 'openvpn'
          ? [
              ['Type', 'OpenVPN'],
              ['Routed networks', policy.openvpn!.routes.join(', ')],
              ['Profile', current.connector_secrets_set?.openvpn_profile ? 'Stored' : 'Missing'],
            ]
          : [
              ['Type', 'IPsec (IKEv2)'],
              ['Gateway', policy.ipsec!.remote_address],
              ['Remote networks', policy.ipsec!.remote_networks.join(', ')],
              ['Pre-shared key', current.connector_secrets_set?.ipsec_psk ? 'Stored' : 'Missing'],
            ]
  return (
    <SettingsSection
      title="VPN connector"
      help="Joins this server to a VPN you already run, as a client. Only the networks you list are routed through it."
      state={state}
      actions={type && action}
    >
      {type ? (
        <InstallationReviewRows rows={rows} />
      ) : (
        <GuidedEmpty
          compact
          icon="lock"
          title="Use the VPN you already have"
          description="Connect this server to WireGuard, Tailscale or Headscale, OpenVPN or IPsec."
          primary={action}
        />
      )}
    </SettingsSection>
  )
}

// ------------------------------------------------------------ Shared form

function TaskForm({
  title,
  label,
  review,
  setReview,
  reviewRows,
  validate,
  save,
  saveLabel,
  children,
}: {
  title: string
  label: string
  review: boolean
  setReview: (value: boolean) => void
  reviewRows: [string, ReactNode][]
  validate: () => string
  save: () => Promise<Access>
  saveLabel: string
  children: ReactNode
}) {
  const navigate = useNavigate()
  const cache = useQueryClient()
  const formRef = useInstallationFormFocus(review)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  return (
    <FormPage title={title} breadcrumbs={[]}>
      <form
        ref={formRef}
        tabIndex={-1}
        aria-label={review ? `${label} review` : label}
        className="settings-task-form"
        onSubmit={async (event) => {
          event.preventDefault()
          if (busy) return
          if (!review) {
            const problem = validate()
            setError(problem)
            if (!problem) setReview(true)
            return
          }
          setBusy(true)
          setError('')
          try {
            cache.setQueryData(accessQueryKey, await save())
            await navigate(toVPN)
          } catch (cause) {
            setError(message(cause))
            if (cause instanceof APIError && cause.status === 409)
              void cache.invalidateQueries({ queryKey: accessQueryKey })
          } finally {
            setBusy(false)
          }
        }}
      >
        {review ? (
          <FormSection title={`Review ${label.toLowerCase()}`}>
            <InstallationReviewRows rows={reviewRows} />
            <p className="field-help">
              The VPN agent applies the change within ten seconds. If the access policy is
              restricted, it waits for confirmation and reverts after five minutes otherwise.
            </p>
          </FormSection>
        ) : (
          children
        )}
        {error && <RequestError error={error} />}
        <div className="form-footer">
          <Button
            type="button"
            variant="outline"
            disabled={busy}
            onClick={() => (review ? setReview(false) : void navigate(toVPN))}
          >
            {review ? 'Back to edit' : 'Cancel'}
          </Button>
          <Button type="submit" variant="primary" disabled={busy} aria-busy={busy || undefined}>
            {busy ? 'Saving…' : review ? saveLabel : 'Review changes'}
          </Button>
        </div>
      </form>
    </FormPage>
  )
}

function EditorLoader({ children }: { children: (current: Access) => ReactNode }) {
  const query = useAccessPolicy()
  if (query.isPending) return <Loading />
  if (query.error || !query.data)
    return <ErrorState error={query.error} retry={() => void query.refetch()} />
  return <>{children(query.data)}</>
}

// --------------------------------------------------------- Server editor

export function VPNServerEditor() {
  return (
    <InstallationAccess>
      <EditorLoader>{(current) => <ServerForm current={current} />}</EditorLoader>
    </InstallationAccess>
  )
}

function ServerForm({ current }: { current: Access }) {
  const server = current.policy.vpn_server
  const [on, setOn] = useState(true)
  const [endpoint, setEndpoint] = useState(server?.endpoint || '')
  const [port, setPort] = useState(String(server?.listen_port || 51820))
  const [network, setNetwork] = useState(server?.network || '10.100.0.0/24')
  const [review, setReview] = useState(false)
  return (
    <TaskForm
      title={server ? 'Hakopod VPN server' : 'Set up the Hakopod VPN server'}
      label="VPN server"
      review={review}
      setReview={setReview}
      validate={() =>
        on && !/^[^\s]+:\d{1,5}$/.test(endpoint.trim())
          ? 'Enter the public host and UDP port devices dial, such as vpn.example.com:51820.'
          : ''
      }
      reviewRows={
        on
          ? [
              ['Server', server ? 'Keep running with these settings' : 'Start'],
              ['Endpoint', endpoint.trim()],
              ['UDP port', port],
              ['VPN network', network.trim()],
              ['Dashboard on the VPN', firstAddress(network.trim())],
            ]
          : [['Server', 'Stop and disconnect every device']]
      }
      saveLabel={on ? 'Save VPN server' : 'Turn off VPN server'}
      save={() =>
        savePolicy(current, {
          vpn_server: on
            ? { endpoint: endpoint.trim(), listen_port: Number(port) || 0, network: network.trim() }
            : undefined,
        })
      }
    >
      <FormSection title="Server">
        {server && (
          <label className="checkbox-label">
            <Input type="checkbox" checked={on} onChange={(event) => setOn(event.target.checked)} />
            Run the VPN server
          </label>
        )}
        {on && (
          <div className="form-grid-two">
            <label>
              Public endpoint
              <Input
                required
                value={endpoint}
                placeholder="vpn.example.com:51820"
                spellCheck={false}
                onChange={(event) => setEndpoint(event.target.value)}
              />
              <small className="field-help">The host name or address and port devices dial.</small>
            </label>
            <label>
              UDP port
              <Input
                type="number"
                min={1}
                max={65535}
                value={port}
                onChange={(event) => setPort(event.target.value)}
              />
              <small className="field-help">Open it in any firewall in front of the server.</small>
            </label>
          </div>
        )}
      </FormSection>
      {on && (
        <FormSection title="Network">
          <label>
            VPN network
            <Input
              value={network}
              spellCheck={false}
              onChange={(event) => setNetwork(event.target.value)}
            />
            <small className="field-help">
              Devices get addresses here and the server takes the first one. Add this network to
              Network access to admit people on the VPN.
            </small>
          </label>
        </FormSection>
      )}
    </TaskForm>
  )
}

// ------------------------------------------------------ Connector editor

export function VPNConnectorEditor() {
  return (
    <InstallationAccess>
      <EditorLoader>{(current) => <ConnectorForm current={current} />}</EditorLoader>
    </InstallationAccess>
  )
}

function ConnectorForm({ current }: { current: Access }) {
  const policy = current.policy
  const [type, setType] = useState<ConnectorType | 'none'>(connectorOf(policy) || 'wireguard')
  const [wgAddress, setWGAddress] = useState(policy.vpn?.address || '')
  const [wgPeer, setWGPeer] = useState(policy.vpn?.peer_public_key || '')
  const [wgEndpoint, setWGEndpoint] = useState(policy.vpn?.peer_endpoint || '')
  const [wgRoutes, setWGRoutes] = useState((policy.vpn?.allowed_ips || []).join(' '))
  const [tsHostname, setTSHostname] = useState(policy.tailscale?.hostname || 'hakopod')
  const [tsTags, setTSTags] = useState((policy.tailscale?.tags || []).join(' '))
  const [tsLogin, setTSLogin] = useState(policy.tailscale?.login_server || '')
  const [ovpnRoutes, setOVPNRoutes] = useState((policy.openvpn?.routes || []).join(' '))
  const [ipRemote, setIPRemote] = useState(policy.ipsec?.remote_address || '')
  const [ipLocalID, setIPLocalID] = useState(policy.ipsec?.local_id || '')
  const [ipRemoteID, setIPRemoteID] = useState(policy.ipsec?.remote_id || '')
  const [ipNetworks, setIPNetworks] = useState((policy.ipsec?.remote_networks || []).join(' '))
  const [action, setAction] = useState<SecretAction>('keep')
  const [secret, setSecret] = useState('')
  const [review, setReview] = useState(false)
  const stored = {
    tailscale: current.connector_secrets_set?.tailscale_auth_key,
    openvpn: current.connector_secrets_set?.openvpn_profile,
    ipsec: current.connector_secrets_set?.ipsec_psk,
  }
  const changingType = type !== (connectorOf(policy) || 'none')
  const secretKey =
    type === 'tailscale'
      ? 'tailscale_auth_key'
      : type === 'openvpn'
        ? 'openvpn_profile'
        : 'ipsec_psk'
  const needsSecret = type === 'tailscale' || type === 'openvpn' || type === 'ipsec'
  const secretSummary = !needsSecret
    ? undefined
    : action === 'replace'
      ? 'Replace with the new value'
      : stored[type as 'tailscale' | 'openvpn' | 'ipsec']
        ? 'Keep the stored value'
        : 'Missing'

  // One connector at a time: clear all four, then set the chosen one.
  const cleared: Partial<AccessPolicy> = {
    vpn: undefined,
    tailscale: undefined,
    openvpn: undefined,
    ipsec: undefined,
  }
  const change: Partial<AccessPolicy> = {
    ...cleared,
    ...(type === 'wireguard'
      ? {
          vpn: {
            address: wgAddress.trim(),
            peer_public_key: wgPeer.trim(),
            peer_endpoint: wgEndpoint.trim(),
            allowed_ips: lines(wgRoutes),
          },
        }
      : type === 'tailscale'
        ? {
            tailscale: {
              hostname: tsHostname.trim(),
              tags: lines(tsTags),
              login_server: tsLogin.trim(),
            },
          }
        : type === 'openvpn'
          ? { openvpn: { routes: lines(ovpnRoutes) } }
          : type === 'ipsec'
            ? {
                ipsec: {
                  remote_address: ipRemote.trim(),
                  local_id: ipLocalID.trim(),
                  remote_id: ipRemoteID.trim(),
                  remote_networks: lines(ipNetworks),
                },
              }
            : {}),
  }
  const secrets: AccessSecrets = action === 'replace' && secret ? { [secretKey]: secret } : {}

  const reviewRows: [string, ReactNode][] =
    type === 'none'
      ? [['Connector', 'Disconnect and remove it']]
      : [
          ['Connector', connectorNames[type]],
          ...(type === 'wireguard'
            ? ([
                ['Tunnel address', wgAddress],
                ['Peer endpoint', wgEndpoint],
                ['Routed networks', lines(wgRoutes).join(', ')],
              ] as [string, ReactNode][])
            : type === 'tailscale'
              ? ([
                  ['Hostname', tsHostname],
                  ['Tags', lines(tsTags).join(', ') || 'None'],
                  ['Login server', tsLogin || 'Tailscale'],
                ] as [string, ReactNode][])
              : type === 'openvpn'
                ? ([['Routed networks', lines(ovpnRoutes).join(', ')]] as [string, ReactNode][])
                : ([
                    ['Gateway', ipRemote],
                    ['Identities', `${ipLocalID} → ${ipRemoteID}`],
                    ['Remote networks', lines(ipNetworks).join(', ')],
                  ] as [string, ReactNode][])),
          ...(secretSummary ? ([['Secret', secretSummary]] as [string, ReactNode][]) : []),
          ...(changingType && connectorOf(policy)
            ? ([['Replaces', connectorNames[connectorOf(policy)!]]] as [string, ReactNode][])
            : []),
        ]

  return (
    <TaskForm
      title="VPN connector"
      label="VPN connector"
      review={review}
      setReview={setReview}
      reviewRows={reviewRows}
      validate={() =>
        needsSecret && action !== 'replace' && !stored[type as 'tailscale' | 'openvpn' | 'ipsec']
          ? 'Add the secret for this connector.'
          : ''
      }
      saveLabel={type === 'none' ? 'Disconnect' : 'Save connector'}
      save={() => savePolicy(current, change, { secrets })}
    >
      <FormSection title="Connector">
        <label>
          Connect this server to
          <SelectField
            label="Connector type"
            value={type}
            onValueChange={(value) => {
              setType(value as ConnectorType | 'none')
              setAction('keep')
              setSecret('')
            }}
            options={[
              { value: 'wireguard', label: 'WireGuard' },
              { value: 'tailscale', label: 'Tailscale or Headscale' },
              { value: 'openvpn', label: 'OpenVPN' },
              { value: 'ipsec', label: 'IPsec (IKEv2, pre-shared key)' },
              ...(connectorOf(policy) ? [{ value: 'none', label: 'No connector' }] : []),
            ]}
          />
          <small className="field-help">
            {type === 'tailscale' || type === 'openvpn' || type === 'ipsec'
              ? `Install the ${connectorNames[type]} client on the server first. Only one connector runs at a time.`
              : 'Only one connector runs at a time.'}
          </small>
        </label>
      </FormSection>
      {type === 'wireguard' && (
        <FormSection title="WireGuard peer">
          <div className="form-grid-two">
            <label>
              This server's tunnel address
              <Input
                value={wgAddress}
                placeholder="10.99.0.2/32"
                spellCheck={false}
                onChange={(e) => setWGAddress(e.target.value)}
              />
            </label>
            <label>
              Peer endpoint
              <Input
                value={wgEndpoint}
                placeholder="vpn.example.com:51820"
                spellCheck={false}
                onChange={(e) => setWGEndpoint(e.target.value)}
              />
            </label>
          </div>
          <label>
            Peer public key
            <Input value={wgPeer} spellCheck={false} onChange={(e) => setWGPeer(e.target.value)} />
          </label>
          <label>
            Networks routed through the tunnel
            <Input
              value={wgRoutes}
              placeholder="10.99.0.0/24"
              spellCheck={false}
              onChange={(e) => setWGRoutes(e.target.value)}
            />
            <small className="field-help">
              The server's own key is generated on the server. After saving, add its public key,
              shown on the VPN page, as a peer.
            </small>
          </label>
        </FormSection>
      )}
      {type === 'tailscale' && (
        <FormSection title="Tailnet">
          <div className="form-grid-two">
            <label>
              Hostname
              <Input
                value={tsHostname}
                spellCheck={false}
                onChange={(e) => setTSHostname(e.target.value)}
              />
            </label>
            <label>
              Tags
              <Input
                value={tsTags}
                placeholder="tag:hakopod"
                spellCheck={false}
                onChange={(e) => setTSTags(e.target.value)}
              />
            </label>
          </div>
          <label>
            Login server
            <Input
              value={tsLogin}
              placeholder="Empty for Tailscale, https://… for Headscale"
              spellCheck={false}
              onChange={(e) => setTSLogin(e.target.value)}
            />
            <small className="field-help">
              The server joins without changing its firewall, DNS or routes.
            </small>
          </label>
        </FormSection>
      )}
      {type === 'openvpn' && (
        <FormSection title="OpenVPN">
          <label>
            Networks routed through the tunnel
            <Input
              value={ovpnRoutes}
              placeholder="10.20.0.0/16"
              spellCheck={false}
              onChange={(e) => setOVPNRoutes(e.target.value)}
            />
            <small className="field-help">Routes pushed by the OpenVPN server are ignored.</small>
          </label>
        </FormSection>
      )}
      {type === 'ipsec' && (
        <FormSection title="IPsec">
          <label>
            Remote gateway
            <Input
              value={ipRemote}
              placeholder="vpn.example.com"
              spellCheck={false}
              onChange={(e) => setIPRemote(e.target.value)}
            />
          </label>
          <div className="form-grid-two">
            <label>
              Local identity
              <Input
                value={ipLocalID}
                placeholder="hakopod.example.com"
                spellCheck={false}
                onChange={(e) => setIPLocalID(e.target.value)}
              />
            </label>
            <label>
              Remote identity
              <Input
                value={ipRemoteID}
                placeholder="vpn.example.com"
                spellCheck={false}
                onChange={(e) => setIPRemoteID(e.target.value)}
              />
            </label>
          </div>
          <label>
            Remote networks
            <Input
              value={ipNetworks}
              placeholder="10.30.0.0/16"
              spellCheck={false}
              onChange={(e) => setIPNetworks(e.target.value)}
            />
          </label>
        </FormSection>
      )}
      {(type === 'tailscale' || type === 'ipsec') && (
        <FormSection title="Secret">
          <InstallationCredential
            label={type === 'tailscale' ? 'Auth key' : 'Pre-shared key'}
            configured={Boolean(stored[type])}
            action={action}
            onAction={setAction}
            value={secret}
            onValue={setSecret}
            maxLength={type === 'tailscale' ? 512 : 256}
          />
        </FormSection>
      )}
      {type === 'openvpn' && (
        <FormSection title="Client profile">
          <label>
            Profile
            <SelectField
              label="Profile action"
              value={action === 'clear' ? 'keep' : action}
              onValueChange={(value) => {
                setAction(value as SecretAction)
                setSecret('')
              }}
              options={[
                {
                  value: 'keep',
                  label: stored.openvpn ? 'Keep stored profile' : 'No stored profile',
                },
                { value: 'replace', label: stored.openvpn ? 'Replace profile' : 'Add profile' },
              ]}
            />
          </label>
          {action === 'replace' && (
            <label>
              Profile (.ovpn)
              <Textarea
                rows={8}
                required
                value={secret}
                spellCheck={false}
                placeholder="Paste the profile with inline certificates and keys"
                onChange={(e) => setSecret(e.target.value)}
              />
            </label>
          )}
          <p className="field-help">
            Scripts, plugins and pushed default routes are refused. The profile is stored encrypted
            and never shown again.
          </p>
        </FormSection>
      )}
    </TaskForm>
  )
}

// ------------------------------------------------------------ New device

export function VPNDeviceEditor() {
  return (
    <InstallationAccess>
      <EditorLoader>{(current) => <DeviceForm current={current} />}</EditorLoader>
    </InstallationAccess>
  )
}

function DeviceForm({ current }: { current: Access }) {
  const navigate = useNavigate()
  const cache = useQueryClient()
  const [name, setName] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [profile, setProfile] = useState<{ name: string; text: string } | null>(null)
  const server = current.policy.vpn_server
  const host = typeof location === 'undefined' ? 'this dashboard' : location.hostname

  function download() {
    if (!profile) return
    const url = URL.createObjectURL(new Blob([profile.text], { type: 'text/plain' }))
    const link = Object.assign(document.createElement('a'), {
      href: url,
      download: `hakopod-${profile.name.replace(/[^A-Za-z0-9_-]+/g, '-')}.conf`,
    })
    link.click()
    URL.revokeObjectURL(url)
  }

  if (!server)
    return (
      <FormPage title="Add a VPN device" breadcrumbs={[]}>
        <GuidedEmpty
          compact
          icon="lock"
          title="Set up the VPN server first"
          description="Devices connect to the Hakopod VPN server."
          primary={
            <Button variant="primary" asChild>
              <Link to="/settings/vpn/setup">Set up</Link>
            </Button>
          }
        />
      </FormPage>
    )

  if (profile)
    return (
      <FormPage title={`${profile.name} is ready`} breadcrumbs={[]}>
        <FormSection title="Download the profile">
          <Note>
            The private key exists only in this page. Download the profile now; it cannot be
            downloaded again. If it is lost, revoke the device and add it again.
          </Note>
          <ol className="m-0 grid gap-2 pl-5 text-sm">
            <li>Download the profile and import it into the WireGuard app on the device.</li>
            <li>
              Point {host} at {firstAddress(server.network)} on the device, with split DNS or the
              hosts file.
            </li>
            <li>Turn the tunnel on and open this dashboard.</li>
          </ol>
          <div className="form-footer">
            <Button variant="outline" onClick={() => void navigate(toVPN)}>
              Done
            </Button>
            <Button variant="primary" onClick={download}>
              Download profile
            </Button>
          </div>
        </FormSection>
      </FormPage>
    )

  return (
    <FormPage title="Add a VPN device" breadcrumbs={[]}>
      <form
        className="settings-task-form"
        aria-label="New VPN device"
        onSubmit={async (event) => {
          event.preventDefault()
          if (busy || !name.trim()) return
          setBusy(true)
          setError('')
          try {
            const keys = await wireguardKeyPair()
            const peer = await unwrap(
              client.POST('/installation/access/vpn-peers', {
                body: { name: name.trim(), public_key: keys.publicKey },
              }),
            )
            setProfile({
              name: peer.name,
              text: wireguardProfile({
                privateKey: keys.privateKey,
                address: peer.address,
                serverPublicKey: peer.server_public_key || '',
                endpoint: peer.endpoint || '',
                network: peer.network || '',
              }),
            })
            void cache.invalidateQueries({ queryKey: ['installation-vpn-peers'] })
          } catch (cause) {
            setError(
              cause instanceof DOMException
                ? 'This browser cannot generate WireGuard keys. Use a current Chrome, Edge, Firefox or Safari.'
                : message(cause),
            )
          } finally {
            setBusy(false)
          }
        }}
      >
        <FormSection title="Device">
          <label>
            Device name
            <Input
              required
              maxLength={64}
              value={name}
              placeholder="Asha's laptop"
              onChange={(event) => setName(event.target.value)}
            />
            <small className="field-help">
              The key pair is generated in this browser. Only the public key is sent to the server.
            </small>
          </label>
        </FormSection>
        {error && <RequestError error={error} />}
        <div className="form-footer">
          <Button
            type="button"
            variant="outline"
            disabled={busy}
            onClick={() => void navigate(toVPN)}
          >
            Cancel
          </Button>
          <Button
            type="submit"
            variant="primary"
            disabled={busy || !name.trim()}
            aria-busy={busy || undefined}
          >
            {busy ? 'Creating…' : 'Create profile'}
          </Button>
        </div>
      </form>
    </FormPage>
  )
}
