export type TerminalEvent =
  { type: 'output'; data: Uint8Array } | { type: 'exit'; code?: number; message?: string }
// A network read can contain many valid frames. Bound each frame and the retained
// incomplete frame, then yield one decoded block at a time for render backpressure.
export async function* readTerminalEvents(
  stream: ReadableStream<Uint8Array>,
  signal: AbortSignal,
): AsyncGenerator<TerminalEvent> {
  const reader = stream.getReader()
  const decoder = new TextDecoder()
  let buffer = ''
  try {
    while (!signal.aborted) {
      const next = await reader.read()
      if (next.done) break
      buffer += decoder.decode(next.value, { stream: true })
      let boundary: number
      while ((boundary = buffer.indexOf('\n\n')) >= 0) {
        const frame = buffer.slice(0, boundary)
        buffer = buffer.slice(boundary + 2)
        if (frame.length > 16 * 1024)
          throw new Error('Terminal output frame exceeded its size limit.')
        const data = frame
          .split('\n')
          .filter((line) => line.startsWith('data:'))
          .map((line) => line.slice(5).trimStart())
          .join('\n')
        if (!data) continue
        const event: unknown = JSON.parse(data)
        if (!event || typeof event !== 'object' || !('type' in event))
          throw new Error('Invalid terminal event.')
        if (event.type === 'output') {
          if (!('data' in event) || typeof event.data !== 'string' || event.data.length > 5500)
            throw new Error('Invalid terminal output block.')
          const bytes = Uint8Array.from(atob(event.data), (char) => char.charCodeAt(0))
          if (bytes.length > 4096) throw new Error('Terminal output block exceeded 4096 bytes.')
          yield { type: 'output', data: bytes }
        } else if (event.type === 'exit') {
          const code = 'code' in event && typeof event.code === 'number' ? event.code : undefined
          const message =
            'message' in event && typeof event.message === 'string'
              ? event.message.slice(0, 4096)
              : undefined
          yield { type: 'exit', code, message }
        }
      }
      if (buffer.length > 16 * 1024)
        throw new Error('Incomplete terminal output frame exceeded its size limit.')
    }
  } finally {
    await reader.cancel().catch(() => undefined)
    reader.releaseLock()
  }
}
