import type { DatabasePlacementNode, DatabaseSpec } from './databases'
import { engineName, placementDomains } from './database-view'

export function databaseEligibleNodes(engine: DatabaseSpec['engine'], nodes: DatabasePlacementNode[]) {
  if (!['mysql', 'mongodb', 'vitess'].includes(engine)) return nodes
  return nodes.map((node) => node.architecture === 'amd64' ? node : {
    ...node, available: false,
    reason: `${engineName(engine)} requires an amd64 node for its supported images.${node.reason ? ` ${node.reason}` : ''}`,
  })
}

export function databasePlacementIssue(spec: DatabaseSpec, nodes: DatabasePlacementNode[], specific: boolean) {
  nodes = databaseEligibleNodes(spec.engine, nodes)
  const selected = spec.placement?.node_names || []
  if (specific && !selected.length) return 'Choose at least one approved node, or use all approved nodes.'
  if (selected.some((name) => !nodes.some((node) => node.name === name && node.available))) return 'A selected node is unavailable or no longer approved. Review the selection before continuing.'
  const candidates = nodes.filter((node) => node.available && (!specific || selected.includes(node.name)))
  if (!candidates.length) return 'No approved database node is currently available.'
  const domains = spec.placement?.spread === 'zones' ? new Set(candidates.map((node) => node.zone).filter(Boolean)).size : candidates.length
  if (spec.placement?.spread && domains < placementDomains(spec)) return `This layout needs ${placementDomains(spec)} distinct ${spec.placement.spread}. Only ${domains} are currently available in this selection.`
}
