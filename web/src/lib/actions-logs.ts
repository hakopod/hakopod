// Workflow text is untrusted. Strip terminal controls, including OSC hyperlinks;
// React still performs the final HTML escaping when rendering each text node.
export function cleanWorkflowLog(value: string) {
  return value
    .replace(/\x1b\][^\x07]*(?:\x07|\x1b\\)/g, '')
    .replace(/\x1b\[[0-?]*[ -/]*[@-~]/g, '')
    .replace(/[\x00-\x08\x0b-\x1f\x7f]/g, '')
}
export function logTimestamp(text: string) {
  const match = text.match(/^(\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d(?:\.\d+)?Z) /)
  if (!match) return null
  const timestamp = Date.parse(match[1])
  return Number.isFinite(timestamp) ? { timestamp, text: text.slice(match[0].length) } : null
}
export function jobDuration(start?: string | null, end?: string | null) {
  if (!start) return 'Not started'
  const seconds = Math.max(
    0,
    Math.floor(((end ? Date.parse(end) : Date.now()) - Date.parse(start)) / 1000),
  )
  return seconds < 60 ? `${seconds}s` : `${Math.floor(seconds / 60)}m ${seconds % 60}s`
}

export type WorkflowLine = { number: number; text: string; rawText?: string }

// Parse only GitHub's group delimiters. Other output remains literal text.
// Iterative ancestry keeps malformed or deeply nested output off the call stack.
export function groupWorkflowLines(lines: WorkflowLine[]) {
  const groups = new Map<
    number,
    {
      title: string
      count: number
      attention: boolean
      attentionLine: number
      unfinished: boolean
      identity: string
    }
  >()
  const rows: (WorkflowLine & { parents: number[] })[] = []
  const parents: number[] = []
  let overflow = 0
  for (const line of lines) {
    const text = logTimestamp(line.text)?.text ?? line.text
    if (text === '##[endgroup]' && parents.length) {
      if (overflow) overflow--
      else parents.pop()
      continue
    }
    for (const id of parents) groups.get(id)!.count++
    if (/^\s*(?:##\[(?:error|warning)\]|error:|fatal:|panic:|npm ERR!|cannot mount )/i.test(text)) {
      for (const id of parents) {
        groups.get(id)!.attention = true
        groups.get(id)!.attentionLine = line.number
      }
    }
    rows.push({ ...line, parents: [...parents] })
    if (text.startsWith('##[group]') && parents.length >= 32) overflow++
    if (text.startsWith('##[group]') && parents.length < 32) {
      groups.set(line.number, {
        title: text.slice(9) || 'Log group',
        count: 0,
        attention: false,
        attentionLine: -1,
        unfinished: false,
        identity: line.text,
      })
      parents.push(line.number)
    }
  }
  // A live or truncated group stays open until its closing delimiter arrives.
  for (const id of parents) {
    groups.get(id)!.unfinished = true
  }
  return { rows, groups }
}

export type WorkflowGroups = ReturnType<typeof groupWorkflowLines>
export type WorkflowExpansion = Record<
  number,
  { identity: string; open: boolean; attention: boolean; attentionLine: number }
>

// Expansion never outlives the retained source window. A newly observed failure
// opens its output even if that group was closed before the failure arrived.
export function reconcileWorkflowExpansion(
  previous: WorkflowExpansion,
  parsed: WorkflowGroups,
  failed: boolean,
  wasFailed: boolean,
): WorkflowExpansion {
  const next: WorkflowExpansion = {}
  let count = 0
  for (const [id, group] of parsed.groups) {
    if (count++ >= 10000) break
    const old = previous[id]?.identity === group.identity ? previous[id] : undefined
    next[id] = {
      identity: group.identity,
      attention: group.attention,
      attentionLine: group.attentionLine,
      open:
        (failed && !wasFailed) || (group.attention && group.attentionLine !== old?.attentionLine)
          ? true
          : (old?.open ?? (failed || group.attention || group.unfinished)),
    }
  }
  return next
}

// Page complete parsed rows, carrying ancestor headers even when search or the
// page boundary excludes them. Ancestor context counts toward the DOM limit.
export function workflowLogPage(parsed: WorkflowGroups, search: string, windowEnd: number | null) {
  const needle = search.toLowerCase()
  const matches = needle
    ? parsed.rows.filter((row) => row.text.toLowerCase().includes(needle))
    : parsed.rows
  const pages: { start: number; end: number; ids: Set<number> }[] = []
  let start = 0
  let ids = new Set<number>()
  for (let i = 0; i < matches.length; i++) {
    const row = matches[i]
    const additions = [row.number, ...row.parents].filter((id) => !ids.has(id))
    if (ids.size + additions.length > 1000) {
      pages.push({ start, end: i, ids })
      start = i
      ids = new Set<number>()
    }
    for (const id of [row.number, ...row.parents]) ids.add(id)
  }
  pages.push({ start, end: matches.length, ids })
  const page =
    windowEnd === null
      ? pages.at(-1)!
      : pages.find((page) => page.end >= windowEnd)! || pages.at(-1)!
  return {
    rows: parsed.rows.filter((row) => page.ids.has(row.number)),
    start: page.start,
    end: page.end,
    total: matches.length,
  }
}

export function workflowLogDownload(lines: WorkflowLine[]) {
  return lines.map((line) => line.rawText ?? line.text).join('\n')
}
