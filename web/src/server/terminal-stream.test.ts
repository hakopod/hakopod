import assert from 'node:assert/strict'
import test from 'node:test'
import { readTerminalEvents } from '../lib/terminal-stream.ts'
const stream = (chunks: string[]) =>
  new ReadableStream<Uint8Array>({
    start(controller) {
      for (const chunk of chunks) controller.enqueue(new TextEncoder().encode(chunk))
      controller.close()
    },
  })
test('terminal accepts many valid frames coalesced beyond 128 KiB in a network read', async () => {
  const block = 'x'.repeat(4096)
  const frame = `data: ${JSON.stringify({ type: 'output', data: btoa(block) })}\n\n`
  let count = 0
  for await (const event of readTerminalEvents(
    stream([frame.repeat(40)]),
    new AbortController().signal,
  )) {
    assert.equal(event.type, 'output')
    if (event.type === 'output') assert.equal(event.data.length, 4096)
    count++
  }
  assert.equal(count, 40)
})
test('terminal frames split across reads retain only incomplete data and decode UTF-8 bytes', async () => {
  const raw = new TextEncoder().encode('λ ready\r\n')
  const frame = `data: ${JSON.stringify({ type: 'output', data: btoa(String.fromCharCode(...raw)) })}\n\n`
  const events = []
  for await (const event of readTerminalEvents(
    stream([frame.slice(0, 7), frame.slice(7), 'data: {"type":"exit","code":0}\n\n']),
    new AbortController().signal,
  ))
    events.push(event)
  assert.equal(events.length, 2)
  assert.equal(events[0].type, 'output')
  if (events[0].type === 'output') assert.deepEqual(events[0].data, raw)
  assert.deepEqual(events[1], { type: 'exit', code: 0, message: undefined })
})
test('terminal rejects oversized decoded output and incomplete frames', async () => {
  for (const input of [
    `data: ${JSON.stringify({ type: 'output', data: btoa('x'.repeat(4097)) })}\n\n`,
    'x'.repeat(16 * 1024 + 1),
  ]) {
    await assert.rejects(async () => {
      for await (const event of readTerminalEvents(stream([input]), new AbortController().signal))
        void event
    }, /exceeded/)
  }
})
