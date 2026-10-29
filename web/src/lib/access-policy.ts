import { useQuery } from '@tanstack/react-query'
import { client, unwrap } from './client'
import type { components } from './api.generated'

export type Access = components['schemas']['InstallationAccess']
export type AccessPolicy = components['schemas']['InstallationAccessPolicy']
export type AccessSecrets = components['schemas']['InstallationAccessSecrets']

export const accessQueryKey = ['installation-access'] as const

export const lines = (value: string) =>
  value
    .split(/[\s,]+/)
    .map((item) => item.trim())
    .filter(Boolean)

export function useAccessPolicy() {
  return useQuery({
    queryKey: accessQueryKey,
    queryFn: ({ signal }) => unwrap(client.GET('/installation/access', { signal })),
    // Poll only while something is settling: a pending confirmation, or the
    // host agent applying a change it has not reported yet.
    refetchInterval: (query) => (query.state.data?.pending_until ? 5000 : 15000),
    refetchOnWindowFocus: false,
    retry: false,
  })
}

// Every page edits one part of the policy; the API takes the whole policy, so
// the rest is sent back unchanged.
export function savePolicy(
  current: Access,
  change: Partial<AccessPolicy>,
  options: { secrets?: AccessSecrets; confirmFromElsewhere?: boolean } = {},
) {
  return unwrap(
    client.PUT('/installation/access', {
      body: {
        expected_revision: current.revision,
        confirm_from_elsewhere: options.confirmFromElsewhere ?? false,
        connector_secrets: options.secrets ?? {},
        policy: { ...current.policy, ...change },
      },
    }),
  )
}

export function confirmPolicy(current: Access) {
  return unwrap(
    client.POST('/installation/access/confirm', { body: { revision: current.revision } }),
  )
}

// The host agent checks in every ten seconds; a minute of silence means it is
// stopped or was never installed.
export function agentState(access: Access): 'missing' | 'stale' | 'running' {
  const seen = access.agent?.seen_at ? new Date(access.agent.seen_at).getTime() : 0
  if (!seen) return 'missing'
  return Date.now() - seen > 60_000 ? 'stale' : 'running'
}

export function ago(at?: string) {
  if (!at) return ''
  const seconds = Math.max(0, Math.round((Date.now() - new Date(at).getTime()) / 1000))
  if (seconds < 90) return `${seconds}s ago`
  if (seconds < 5400) return `${Math.round(seconds / 60)} min ago`
  return `${Math.round(seconds / 3600)} h ago`
}

// The VPN server takes the first address of its IPv4 network.
export function firstAddress(network: string) {
  const [base] = network.split('/')
  const value = base.split('.').reduce((total, part) => total * 256 + Number(part), 0) + 1
  return [24, 16, 8, 0].map((shift) => Math.floor(value / 2 ** shift) % 256).join('.')
}

export const installCommand = 'sudo python3 ./installer/modules.py private-access'
