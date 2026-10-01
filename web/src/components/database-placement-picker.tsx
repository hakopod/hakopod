import type { DatabasePlacementNode } from '../lib/databases'
import { Button } from './ui/button'
import { SelectField } from './ui/select'

export function DatabasePlacementPicker({ nodes, selected, specific, disabled, onSelection, onSpecific }: {
  nodes: DatabasePlacementNode[]
  selected: string[]
  specific: boolean
  disabled: boolean
  onSelection: (names: string[]) => void
  onSpecific: (specific: boolean) => void
}) {
  const missing = selected.filter((name) => !nodes.some((node) => node.name === name))
  return <div className="grid min-w-0 gap-3">
    <label>Eligible nodes<SelectField label="Database node selection" value={specific ? 'specific' : 'all'} disabled={disabled} onValueChange={(value) => onSpecific(value === 'specific')} options={[{ value: 'all', label: 'All approved nodes' }, { value: 'specific', label: 'Choose specific nodes' }]} /></label>
    <p className="field-help">{specific ? 'Select the nodes that may run this database. Unavailable selections stay visible until you remove them.' : 'The scheduler can use any approved node that meets the database requirements.'} Placement is fixed at creation.</p>
    {missing.map((name) => <div key={name} className="flex min-w-0 flex-wrap items-center justify-between gap-2 rounded border border-destructive/40 p-3"><span className="min-w-0 break-all text-sm">{name} · No longer in this allocation</span><Button disabled={disabled} onClick={() => onSelection(selected.filter((value) => value !== name))} aria-label={`Remove unavailable node ${name}`}>Remove</Button></div>)}
    <div role="region" aria-label="Approved database nodes" tabIndex={0} className="grid max-h-80 min-w-0 gap-2 overflow-y-auto outline-offset-2 focus-visible:outline-2 focus-visible:outline-ring sm:grid-cols-2">
      {nodes.map((node) => <label key={node.name} className="flex min-w-0 items-start gap-3 rounded border border-border p-3">
        {specific && <input type="checkbox" className="mt-1 h-4 w-4 shrink-0" checked={selected.includes(node.name)} disabled={disabled || !node.available && !selected.includes(node.name)} onChange={(event) => onSelection(event.target.checked ? [...selected, node.name] : selected.filter((name) => name !== node.name))} aria-label={`Allow ${node.name}`} />}
        <span className="grid min-w-0 gap-1">
          <strong className="break-all text-sm font-medium">{node.name}</strong>
          <span className="text-xs text-muted-foreground">{node.architecture || 'Architecture unknown'} · {node.available ? 'Available' : 'Unavailable'}</span>
          <span className="break-words text-xs text-muted-foreground">{[node.provider, node.region, node.zone].filter(Boolean).join(' / ') || 'Failure domain not reported'}</span>
          {node.reason && <span className="text-xs">{node.reason}</span>}
          {node.reserved_cpu_milli !== undefined && node.reserved_memory_bytes !== undefined && <span className="text-xs text-muted-foreground">Workspace reservation: {(node.reserved_cpu_milli / 1000).toLocaleString()} CPU · {(node.reserved_memory_bytes / 1073741824).toLocaleString(undefined, { maximumFractionDigits: 2 })} GiB memory</span>}
        </span>
      </label>)}
    </div>
    {!nodes.length && <p className="text-sm">No database nodes are available in this allocation.</p>}
  </div>
}
