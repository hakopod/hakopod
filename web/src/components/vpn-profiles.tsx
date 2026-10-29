import { useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { client, unwrap } from '../lib/client'
import { message } from '../lib/api'
import { wireguardKeyPair, wireguardProfile } from '../lib/wireguard'
import { Button } from './ui/button'
import { Field } from './ui/field'
import { ErrorState, Loading, Note, RequestError } from './shared'
import { SettingsSection } from './settings-section'
import { GuidedEmpty } from './resource-card'

function download(name: string, text: string) {
  const url = URL.createObjectURL(new Blob([text], { type: 'text/plain' }))
  const link = Object.assign(document.createElement('a'), { href: url, download: name })
  link.click()
  URL.revokeObjectURL(url)
}

function handshake(at?: string) {
  if (!at) return 'No handshake yet'
  const seconds = Math.round((Date.now() - new Date(at).getTime()) / 1000)
  return seconds < 180
    ? `Connected ${seconds}s ago`
    : `Last seen ${Math.round(seconds / 60)} min ago`
}

// Each device gets its own key pair, generated here. The server stores only
// the public key, so a profile can be downloaded once and never again.
export function VPNProfiles({ serverAddress }: { serverAddress: string }) {
  const cache = useQueryClient()
  const query = useQuery({
    queryKey: ['installation-vpn-peers'],
    queryFn: ({ signal }) => unwrap(client.GET('/installation/access/vpn-peers', { signal })),
    refetchInterval: 15000,
    retry: false,
  })
  const [name, setName] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [created, setCreated] = useState('')

  async function create() {
    if (busy || !name.trim()) return
    setBusy(true)
    setError('')
    setCreated('')
    try {
      const keys = await wireguardKeyPair()
      const peer = await unwrap(
        client.POST('/installation/access/vpn-peers', {
          body: { name: name.trim(), public_key: keys.publicKey },
        }),
      )
      download(
        `hakopod-${peer.name.replace(/[^A-Za-z0-9_-]+/g, '-')}.conf`,
        wireguardProfile({
          privateKey: keys.privateKey,
          address: peer.address,
          serverPublicKey: peer.server_public_key || '',
          endpoint: peer.endpoint || '',
          network: peer.network || '',
        }),
      )
      setCreated(`Downloaded the profile for ${peer.name}. It cannot be downloaded again.`)
      setName('')
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
  }

  async function revoke(id: string) {
    setError('')
    try {
      await unwrap(
        client.DELETE('/installation/access/vpn-peers/{id}', { params: { path: { id } } }),
      )
      void cache.invalidateQueries({ queryKey: ['installation-vpn-peers'] })
    } catch (cause) {
      setError(message(cause))
    }
  }

  const peers = query.data?.items || []
  return (
    <SettingsSection
      level={3}
      title="VPN profiles"
      help="One profile per device. The key pair is generated in this browser and the private key goes only into the downloaded file. Import it into the WireGuard app."
      state={`${peers.length} ${peers.length === 1 ? 'device' : 'devices'}`}
    >
      {query.isPending ? (
        <Loading />
      ) : query.error ? (
        <ErrorState error={query.error} retry={() => void query.refetch()} />
      ) : peers.length === 0 ? (
        <GuidedEmpty
          compact
          icon="lock"
          title="Create your first device profile"
          description="Name the device, download its profile and import it into WireGuard."
        />
      ) : (
        <ul className="m-0 grid list-none gap-1 p-0" aria-label="VPN devices">
          {peers.map((peer) => (
            <li key={peer.id} className="flex items-center justify-between gap-3 py-2 text-sm">
              <span>
                <strong>{peer.name}</strong> · {peer.address} · {handshake(peer.latest_handshake)}
              </span>
              <Button size="sm" variant="outline" onClick={() => void revoke(peer.id)}>
                Revoke
              </Button>
            </li>
          ))}
        </ul>
      )}
      <Note>
        On VPN devices, point the dashboard host name at {serverAddress} (split DNS or the hosts
        file). The server forwards the dashboard and the Kubernetes API from that address.
      </Note>
      <div className="flex flex-wrap items-end gap-2">
        <Field
          label="Device name"
          value={name}
          maxLength={64}
          placeholder="For example Asha's laptop"
          onChange={(event) => setName(event.target.value)}
        />
        <Button variant="primary" disabled={busy || !name.trim()} onClick={() => void create()}>
          {busy ? 'Creating…' : 'Create and download profile'}
        </Button>
      </div>
      {created && (
        <p role="status" className="m-0 text-sm">
          {created}
        </p>
      )}
      {error && <RequestError error={error} />}
    </SettingsSection>
  )
}
