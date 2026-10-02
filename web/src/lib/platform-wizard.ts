import type {
  ManagedPlatformCatalog,
  ManagedPlatformDefaults,
  ManagedPlatformSpec,
} from './managed-platforms'

export type PlatformStage =
  'Platform' | 'Settings' | 'Placement' | 'Secrets' | 'Resources' | 'Review'
export type PlatformStep = { id: string; title: string; stage: PlatformStage }

export function platformSteps(kind: string, secretKeys: string[]): PlatformStep[] {
  const settings =
    kind === 'supabase'
      ? [
          { id: 'connections', title: 'Application URLs' },
          { id: 'database', title: 'Database and API' },
          { id: 'pool', title: 'Connections and signup' },
        ]
      : [
          { id: 'topology', title: 'Compute and storage nodes' },
          { id: 'object-storage', title: 'Object storage' },
        ]
  return [
    { id: 'platform', title: 'Choose a platform', stage: 'Platform' },
    ...settings.map((step) => ({ ...step, stage: 'Settings' as const })),
    { id: 'placement', title: 'Choose nodes', stage: 'Placement' },
    ...Array.from({ length: Math.ceil(secretKeys.length / 4) }, (_, index) => ({
      id: `secrets-${index + 1}`,
      title: `Secret references ${index + 1} of ${Math.ceil(secretKeys.length / 4)}`,
      stage: 'Secrets' as const,
    })),
    { id: 'resources', title: 'Resources and storage', stage: 'Resources' },
    { id: 'review', title: 'Review platform', stage: 'Review' },
  ]
}

export function completePlatformSpec(
  draft: ManagedPlatformDefaults,
  nodes: string[],
  keys: string[],
  selected: Record<string, string>,
  references: ManagedPlatformCatalog['secret_references'],
): ManagedPlatformSpec | null {
  const secrets: Record<string, { name: string; revision: number }> = {}
  for (const key of keys) {
    const reference = references.find((item) => `${item.name}@${item.revision}` === selected[key])
    if (!reference) return null
    secrets[key] = { name: reference.name, revision: reference.revision }
  }
  if (draft.kind === 'supabase')
    return { ...draft, secrets, placement: { node_names: nodes, spread: '' } }
  return { ...draft, secrets, placement: { node_names: nodes, spread: draft.placement.spread } }
}
