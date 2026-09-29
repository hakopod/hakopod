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

type WorkflowGroup = {
  title: string
  count: number
  attention: boolean
  attentionLine: number
  unfinished: boolean
  containsUnfinished: boolean
  identity: string
  provider: 'github' | 'gitlab'
  sectionId?: string
  collapsed: boolean
  startedAt?: number
  completedAt?: number
  durationSeconds?: number
}

type Section = {
  kind: 'start' | 'end'
  provider: WorkflowGroup['provider']
  id?: string
  timestamp?: number
  title?: string
  collapsed?: boolean
}

function nativeSection(raw: string): Section | undefined {
  const text = logTimestamp(raw)?.text ?? raw
  // GitLab's carriage return and erase-line marker are part of its protocol.
  // Match them before cleanup; ordinary text that mentions a section stays text.
  const match = text.match(
    /^(?:\x1b\[[0-?]*[ -/]*[@-~])*section_(start|end):([0-9]{1,13}):([A-Za-z0-9_.-]{1,255})(?:\[collapsed=(true|false)\])?\r\x1b\[0K([^\r\n]*)\r?$/,
  )
  if (match) {
    const timestamp = Number(match[2]) * 1000
    if (!Number.isSafeInteger(timestamp) || !Number.isFinite(new Date(timestamp).getTime()))
      return undefined
    if (match[1] === 'end' && (match[4] !== undefined || match[5] !== '')) return undefined
    return {
      kind: match[1] as Section['kind'],
      provider: 'gitlab',
      id: match[3],
      timestamp,
      title: cleanWorkflowLog(match[5]) || match[3],
      collapsed: match[4] === 'true',
    }
  }
  const cleaned = cleanWorkflowLog(text)
  if (cleaned === '##[endgroup]') return { kind: 'end', provider: 'github' }
  if (cleaned.startsWith('##[group]'))
    return { kind: 'start', provider: 'github', title: cleaned.slice(9) || 'Log group', collapsed: true }
}

// Match native sections by provider and ID. Line numbers distinguish repeated
// occurrences of the same ID. Iterative ancestry is capped at 32 visible levels.
export function groupWorkflowLines(lines: WorkflowLine[]) {
  const groups = new Map<number, WorkflowGroup>()
  const rows: (WorkflowLine & { parents: number[] })[] = []
  const parents: number[] = []
  let overflow: { section: Section; depth: number } | undefined
  const matches = (section: Section, provider: WorkflowGroup['provider'], id?: string) =>
    section.provider === provider && section.id === id
  for (const line of lines) {
    const raw = line.rawText ?? line.text
    const section = nativeSection(raw)
    let display = cleanWorkflowLog(raw)
    const text = logTimestamp(display)?.text ?? display
    // Keep excess nesting literal without letting its end markers close a
    // shallower section. Only the overflow root and repeated IDs need tracking.
    const overflowing = Boolean(overflow)
    if (overflow && section && matches(section, overflow.section.provider, overflow.section.id)) {
      overflow.depth += section.kind === 'start' ? 1 : -1
      if (overflow.depth === 0) overflow = undefined
    }
    if (!overflowing && section?.kind === 'end') {
      let index = parents.length - 1
      while (index >= 0) {
        const group = groups.get(parents[index])!
        if (matches(section, group.provider, group.sectionId)) break
        index--
      }
      if (index !== -1) {
        const group = groups.get(parents[index])!
        // A reversed clock does not supply a valid section end or duration.
        if (section.timestamp === undefined || section.timestamp >= group.startedAt!) {
          for (const id of parents.slice(index + 1)) groups.get(id)!.unfinished = true
          if (section.timestamp !== undefined) {
            group.completedAt = section.timestamp
            group.durationSeconds = (section.timestamp - group.startedAt!) / 1000
          }
          parents.length = index
          continue
        }
      }
    }
    for (const id of parents) groups.get(id)!.count++
    if (/^\s*(?:##\[(?:error|warning)\]|error:|fatal:|panic:|npm ERR!|cannot mount )/i.test(text)) {
      for (const id of parents) {
        groups.get(id)!.attention = true
        groups.get(id)!.attentionLine = line.number
      }
    }
    const start = !overflowing && section?.kind === 'start' ? section : undefined
    if (start && parents.length < 32) {
      if (start.provider === 'gitlab') {
        const timestamp = logTimestamp(raw)
        const prefix = timestamp
          ? raw.slice(0, raw.length - timestamp.text.length)
          : `${new Date(start.timestamp!).toISOString()} `
        display = prefix + start.title
      }
    }
    rows.push({ ...line, rawText: raw, text: display, parents: [...parents] })
    if (start && parents.length >= 32) overflow = { section: start, depth: 1 }
    if (start && parents.length < 32) {
      groups.set(line.number, {
        title: start.title!,
        count: 0,
        attention: false,
        attentionLine: -1,
        unfinished: false,
        containsUnfinished: false,
        identity: raw,
        provider: start.provider,
        sectionId: start.id,
        collapsed: start.collapsed!,
        startedAt: start.timestamp,
      })
      parents.push(line.number)
    }
  }
  // A live or truncated group stays open until its closing delimiter arrives.
  for (const id of parents) {
    groups.get(id)!.unfinished = true
  }
  for (const row of rows) {
    if (groups.get(row.number)?.unfinished) {
      for (const id of row.parents) groups.get(id)!.containsUnfinished = true
    }
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
          : (old?.open ??
            (failed || group.attention || group.unfinished || group.containsUnfinished || !group.collapsed)),
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
