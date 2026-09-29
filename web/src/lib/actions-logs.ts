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
  return match ? { timestamp: Date.parse(match[1]), text: text.slice(match[0].length) } : null
}
export function jobDuration(start?: string | null, end?: string | null) {
  if (!start) return 'Not started'
  const seconds = Math.max(
    0,
    Math.floor(((end ? Date.parse(end) : Date.now()) - Date.parse(start)) / 1000),
  )
  return seconds < 60 ? `${seconds}s` : `${Math.floor(seconds / 60)}m ${seconds % 60}s`
}

export type WorkflowLine = { number: number; text: string }

// Parse only GitHub's group delimiters. Other output remains literal text.
// Iterative ancestry keeps malformed or deeply nested output off the call stack.
export function groupWorkflowLines(lines: WorkflowLine[]) {
  const groups = new Map<number, { title: string; count: number; attention: boolean }>()
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
    if (/^(?:##\[(?:error|warning)\]|error:|fatal:|cannot mount )/i.test(text)) {
      for (const id of parents) groups.get(id)!.attention = true
    }
    rows.push({ ...line, parents: [...parents] })
    if (text.startsWith('##[group]') && parents.length >= 32) overflow++
    if (text.startsWith('##[group]') && parents.length < 32) {
      groups.set(line.number, { title: text.slice(9) || 'Log group', count: 0, attention: false })
      parents.push(line.number)
    }
  }
  // A live or truncated group stays open until its closing delimiter arrives.
  for (const id of parents) groups.get(id)!.attention = true
  return { rows, groups }
}
