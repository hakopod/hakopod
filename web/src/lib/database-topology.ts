import type { DatabaseConnectionReference } from './databases'

export const topologyApplicationPageSize = 15
export function connectedApplications(connections: DatabaseConnectionReference[]) {
  const groups = new Map<
    string,
    {
      id: string
      name: string
      displayName: string
      project: string
      environment: string
      bindings: DatabaseConnectionReference[]
    }
  >()
  for (const binding of connections) {
    let group = groups.get(binding.application_id)
    if (!group) {
      group = {
        id: binding.application_id,
        name: binding.application_name,
        displayName: binding.application_display_name || binding.application_name,
        project: binding.project,
        environment: binding.environment,
        bindings: [],
      }
      groups.set(group.id, group)
    }
    group.bindings.push(binding)
  }
  return [...groups.values()].sort(
    (a, b) => a.name.localeCompare(b.name) || a.id.localeCompare(b.id),
  )
}

// Fixed-size targets retain legible labels. Large graphs scroll inside the
// canvas; no member is discarded to make it fit the available screen width.
export function databaseTopologyLayout(applications: number, members: number, hasPrimary: boolean, endpoints = 2, poolers = 0, coordinators = 0) {
  const appColumns = Math.min(3, Math.max(1, applications))
  const applicationWidth = appColumns * 180 + 40
  const endpointX = applicationWidth + 30
  const poolerX = endpointX + 215
  const memberStart = poolerX + (poolers ? 215 : 0)
  const memberColumns = Math.min(3, Math.max(1, coordinators, members - (hasPrimary ? 1 : 0)))
  const memberWidth = memberColumns * 180
  const width = memberStart + memberWidth + 20
  const appPositions = Array.from({ length: applications }, (_, i) => ({
    x: 28 + (i % appColumns) * 180,
    y: 88 + Math.floor(i / appColumns) * 104,
  }))
  const memberPositions = Array.from({ length: members }, (_, i) => {
    if (hasPrimary && i === 0) return { x: memberStart + memberWidth / 2, y: 154 }
    const replica = i - (hasPrimary ? 1 : 0)
    return {
      x: memberStart + 90 + (replica % memberColumns) * 180,
      y: (hasPrimary ? 348 : 154) + Math.floor(replica / memberColumns) * 190,
    }
  })
  const endpointPositions = Array.from({ length: endpoints }, (_, i) => ({ x: endpointX, y: 114 + i * 130 }))
  const poolerPositions = Array.from({ length: poolers }, (_, i) => ({ x: poolerX, y: 88 + i * 104 }))
  const coordinationY = Math.max(320, (memberPositions.at(-1)?.y || 154) + 180)
  const coordinatorPositions = Array.from({ length: coordinators }, (_, i) => ({ x: memberStart + 8 + i * 180, y: coordinationY }))
  const height = Math.max(
    400,
    (endpointPositions.at(-1)?.y || 0) + 100,
    (poolerPositions.at(-1)?.y || 0) + 100,
    (appPositions.at(-1)?.y || 0) + 120,
    (memberPositions.at(-1)?.y || 0) + 125,
    coordinators ? coordinationY + 100 : 0,
  )
  return { width, height, applicationWidth, endpointX, memberStart, appPositions, memberPositions, endpointPositions, poolerX, poolerPositions, coordinatorPositions, coordinationY }
}

export function bindingEvidence(binding: DatabaseConnectionReference) {
  return [
    binding.saved_revision > 0 ? `Saved · r${binding.saved_revision}` : '',
    binding.last_successful_revision > 0
      ? `Last successful deployment · r${binding.last_successful_revision}`
      : '',
    binding.latest_attempt_revision > 0
      ? `${binding.latest_attempt_status.replaceAll('_', ' ')} attempt · r${binding.latest_attempt_revision}`
      : '',
  ]
    .filter(Boolean)
    .join(' / ')
}

export function endpointTargetsMember(purpose: string, role: string, engine?: string) {
  if (purpose === 'cluster' && engine === 'mongodb') return role === 'primary' || role === 'replica'
  if (purpose === 'cluster' && engine === 'clickhouse') return role === 'replica'
  return ((purpose === 'read_write' || purpose === 'pooled_read_write' || purpose === 'cluster') && role === 'primary') || ((purpose === 'read_only' || purpose === 'pooled_read_only') && role === 'replica')
}
