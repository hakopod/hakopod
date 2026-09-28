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
