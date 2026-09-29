import { useState } from 'react'
import { ChevronDown, ChevronRight } from 'lucide-react'
import { groupWorkflowLines, logTimestamp, type WorkflowLine } from '../lib/actions-logs'

export function WorkflowLogLines({
  lines,
  wrap,
  timestamps,
  searching,
}: {
  lines: WorkflowLine[]
  wrap: boolean
  timestamps: boolean
  searching: boolean
}) {
  const [expanded, setExpanded] = useState<Record<number, boolean>>({})
  const { rows, groups } = groupWorkflowLines(lines)
  const isOpen = (id: number) => searching || (expanded[id] ?? groups.get(id)!.attention)
  return rows.map((row) => {
    if (row.parents.some((id) => !isOpen(id))) return null
    const group = groups.get(row.number)
    const parsed = logTimestamp(row.text)
    const text = parsed?.text ?? row.text
    const content = group ? group.title : text
    const cells = (
      <>
        <span
          className="muted-text border-r border-border px-2 text-right select-none"
          aria-hidden="true"
        >
          {row.number}
        </span>
        {timestamps && !group && (
          <span className="muted-text border-r border-border px-2 whitespace-nowrap">
            {parsed ? new Date(parsed.timestamp).toISOString().slice(11, 23) : ''}
          </span>
        )}
        <span
          className={`min-w-0 px-3 ${wrap ? 'whitespace-pre-wrap wrap-anywhere' : 'whitespace-pre'}`}
        >
          {group &&
            (isOpen(row.number) ? (
              <ChevronDown className="inline mr-2" size={14} aria-hidden="true" />
            ) : (
              <ChevronRight className="inline mr-2" size={14} aria-hidden="true" />
            ))}
          {content}
          {group && (
            <span className="muted-text ml-3">
              {group.count} {group.count === 1 ? 'line' : 'lines'}
              {group.attention ? ' · Check output' : ''}
            </span>
          )}
        </span>
      </>
    )
    const className = `grid ${timestamps && !group ? 'grid-cols-[2.5rem_6.5rem_minmax(0,1fr)] sm:grid-cols-[3.5rem_7rem_minmax(0,1fr)]' : 'grid-cols-[2.5rem_minmax(0,1fr)] sm:grid-cols-[3.5rem_minmax(0,1fr)]'} border-b border-border text-left min-w-full ${wrap ? 'w-full' : 'w-max'} ${group ? 'py-2 font-medium focus-visible:outline-solid focus-visible:outline-2 focus-visible:-outline-offset-2' : 'py-0.5'}`
    return group ? (
      <button
        type="button"
        key={row.number}
        className={className}
        aria-expanded={isOpen(row.number)}
        onClick={() =>
          setExpanded((previous) => ({ ...previous, [row.number]: !isOpen(row.number) }))
        }
      >
        {cells}
      </button>
    ) : (
      <div key={row.number} className={className}>
        {cells}
      </div>
    )
  })
}
