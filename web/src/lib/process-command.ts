export function formatProcessCommand(words?: string[]) {
  return (words || [])
    .map((word) =>
      /^[a-zA-Z0-9_./:@=-]+$/.test(word) ? word : "'" + word.replaceAll("'", "'\\''") + "'",
    )
    .join(' ')
}
export function parseProcessCommand(text: string): string[] {
  const result: string[] = []
  let word = '',
    quote = '',
    escaped = false,
    started = false
  for (const char of text) {
    if (escaped) {
      word += char
      escaped = false
      started = true
      continue
    }
    if (char === '\\' && quote !== "'") {
      escaped = true
      started = true
      continue
    }
    if (quote) {
      if (char === quote) quote = ''
      else word += char
      continue
    }
    if (char === '"' || char === "'") {
      quote = char
      started = true
      continue
    }
    if (/\s/.test(char)) {
      if (started) {
        result.push(word)
        word = ''
        started = false
      }
      continue
    }
    word += char
    started = true
  }
  if (quote || escaped) throw new Error('Close quotes and complete escapes in the runtime command.')
  if (started) result.push(word)
  return result
}
