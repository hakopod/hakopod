import assert from 'node:assert/strict'
import test from 'node:test'
import {
  cleanWorkflowLog,
  groupWorkflowLines,
  logTimestamp,
  reconcileWorkflowExpansion,
  workflowLogDownload,
  workflowLogPage,
  workflowStepOutput,
} from './actions-logs'

const start = (id: string, time = 1760000000, title = id, collapsed?: boolean) =>
  `\x1b[0Ksection_start:${time}:${id}${collapsed === undefined ? '' : `[collapsed=${collapsed}]`}\r\x1b[0K${title}`
const end = (id: string, time = 1760000003) => `\x1b[0Ksection_end:${time}:${id}\r\x1b[0K`
const lines = (texts: string[]) => texts.map((text, index) => ({ number: index + 1, text }))
const parse = (texts: string[]) => groupWorkflowLines(lines(texts))

test('GitLab sections retain native IDs, timestamps, durations and collapsed defaults', () => {
  const parsed = parse([
    start('service_probe', 1760000000, 'Service container probe', true),
    'PONG',
    end('service_probe'),
  ])
  const group = parsed.groups.get(1)!
  assert.equal(group.provider, 'gitlab')
  assert.equal(group.sectionId, 'service_probe')
  assert.equal(group.title, 'Service container probe')
  assert.equal(group.startedAt, 1760000000000)
  assert.equal(group.completedAt, 1760000003000)
  assert.equal(group.durationSeconds, 3)
  assert.equal(group.unfinished, false)
  assert.equal(group.count, 1)
  assert.deepEqual(
    parsed.rows.map((row) => row.number),
    [1, 2],
  )
  assert.deepEqual(parsed.rows[1].parents, [1])
  assert.equal(logTimestamp(parsed.rows[0].text)?.timestamp, group.startedAt)
  assert.equal(reconcileWorkflowExpansion({}, parsed, false, false)[1].open, false)
  for (const collapsed of [undefined, false]) {
    const expanded = parse([start('open', 1760000000, 'Open', collapsed), end('open')])
    assert.equal(reconcileWorkflowExpansion({}, expanded, false, false)[1].open, true)
  }
})

test('GitLab parses original control bytes before ANSI cleanup and keeps download bytes', () => {
  const raw = [
    start('build', 1760000000, '\x1b[1;36mBuild <script>literal</script>\x1b[0m', true),
    '\x1b]8;;https://example.invalid\x07link\x1b]8;;\x07',
    end('build'),
  ]
  const received = lines(raw).map((line) => ({
    ...line,
    rawText: line.text,
    text: cleanWorkflowLog(line.text),
  }))
  const parsed = groupWorkflowLines(received)
  assert.equal(parsed.groups.get(1)?.title, 'Build <script>literal</script>')
  assert.equal(parsed.rows[1].text, 'link')
  assert.equal(parsed.rows[0].rawText, raw[0])
  assert.equal(workflowLogDownload(received), raw.join('\n'))
  assert.equal(parse(raw.map(cleanWorkflowLog)).groups.size, 0)
})

test('GitLab section headers preserve an existing precise log timestamp', () => {
  const prefix = '2026-09-29T06:46:28.0356127Z '
  const parsed = parse([prefix + start('checkout'), prefix + end('checkout')])
  assert.equal(parsed.rows[0].text, prefix + 'checkout')
  assert.equal(parsed.groups.get(1)?.startedAt, 1760000000000)
  assert.equal(parsed.groups.get(1)?.durationSeconds, 3)
})

test('GitLab sections survive server-side secret masking and ANSI removal', () => {
  const parsed = parse([
    'section_start:1760000000:checkout[collapsed=true]\rFetching source',
    'Authorization: ***',
    'section_end:1760000003:checkout\r',
  ])
  assert.equal(parsed.groups.get(1)?.title, 'Fetching source')
  assert.equal(parsed.groups.get(1)?.durationSeconds, 3)
  assert.equal(parsed.groups.get(1)?.unfinished, false)
  assert.deepEqual(parsed.rows[1].parents, [1])
})

test('GitLab matches nested section IDs instead of closing the last section blindly', () => {
  const parsed = parse([
    start('outer'),
    start('inner'),
    'nested output',
    end('unknown'),
    end('inner', 1760000002),
    'outer output',
    end('outer'),
    'after sections',
  ])
  assert.deepEqual(parsed.rows.find((row) => row.number === 3)?.parents, [1, 2])
  assert.deepEqual(parsed.rows.find((row) => row.number === 4)?.parents, [1, 2])
  assert.deepEqual(parsed.rows.find((row) => row.number === 6)?.parents, [1])
  assert.deepEqual(parsed.rows.at(-1)?.parents, [])
  assert.equal(parsed.groups.get(2)?.durationSeconds, 2)
  assert.equal(parsed.groups.get(1)?.durationSeconds, 3)
})

test('duplicate GitLab IDs close the nearest occurrence and can be reused later', () => {
  const parsed = parse([
    start('same', 1760000000, 'Outer'),
    start('same', 1760000001, 'Inner'),
    end('same', 1760000002),
    'outer only',
    end('same', 1760000003),
    start('same', 1760000004, 'Later'),
    end('same', 1760000005),
  ])
  assert.equal(parsed.groups.size, 3)
  assert.equal(parsed.groups.get(1)?.durationSeconds, 3)
  assert.equal(parsed.groups.get(2)?.durationSeconds, 1)
  assert.equal(parsed.groups.get(6)?.durationSeconds, 1)
  assert.deepEqual(parsed.rows.find((row) => row.number === 4)?.parents, [1])
  assert.notEqual(parsed.groups.get(1)?.identity, parsed.groups.get(6)?.identity)
})

test('out-of-order GitLab ends leave interrupted children unfinished without invented duration', () => {
  const parsed = parse([
    start('outer', 1760000000, 'Outer', true),
    start('inner', 1760000001, 'Inner', true),
    'inside',
    end('outer'),
    end('inner'),
    'after',
  ])
  assert.equal(parsed.groups.get(1)?.unfinished, false)
  assert.equal(parsed.groups.get(2)?.unfinished, true)
  assert.equal(parsed.groups.get(2)?.completedAt, undefined)
  assert.equal(parsed.groups.get(2)?.durationSeconds, undefined)
  assert.equal(parsed.groups.get(1)?.containsUnfinished, true)
  assert.equal(reconcileWorkflowExpansion({}, parsed, false, false)[1].open, true)
  assert.deepEqual(parsed.rows.find((row) => row.number === 5)?.parents, [])
  assert.deepEqual(parsed.rows.at(-1)?.parents, [])
})

test('malformed GitLab markers stay literal and cannot consume ordinary output', () => {
  const text = [
    'section_start:1760000000:plain Header without control bytes',
    'echo ' + start('quoted'),
    start('bad/id'),
    start('bad id'),
    start('x'.repeat(256)),
    start('invalid', 9999999999999),
    start('invalid', 1760000000, 'Header').replace(':1760000000:', ':not-a-time:'),
    start('invalid', 1760000000, 'Header', true).replace('collapsed=true', 'collapsed=maybe'),
    end('missing'),
    end('missing') + 'Keep this output',
    '##[endgroup]',
  ]
  const parsed = parse(text)
  assert.equal(parsed.groups.size, 0)
  assert.deepEqual(
    parsed.rows.map((row) => row.text),
    text.map(cleanWorkflowLog),
  )
})

test('GitLab cannot close a section before its start and accepts a known zero duration', () => {
  const parsed = parse([start('clock'), end('clock', 1759999999), end('clock', 1760000000)])
  assert.deepEqual(
    parsed.rows.map((row) => row.number),
    [1, 2],
  )
  assert.deepEqual(parsed.rows[1].parents, [1])
  assert.equal(parsed.groups.get(1)?.durationSeconds, 0)
  assert.equal(parsed.groups.get(1)?.completedAt, 1760000000000)
})

test('GitLab live sections and new failures reveal output despite collapsed preferences', () => {
  const first = [start('compile', 1760000000, 'Compile', true), 'building']
  const live = parse(first)
  let state = reconcileWorkflowExpansion({}, live, false, false)
  assert.equal(live.groups.get(1)?.unfinished, true)
  assert.equal(live.groups.get(1)?.durationSeconds, undefined)
  assert.equal(state[1].open, true)
  state[1].open = false
  const failed = parse([...first, '\x1b[31merror: compilation failed\x1b[0m', end('compile')])
  state = reconcileWorkflowExpansion(state, failed, false, false)
  assert.equal(failed.groups.get(1)?.attention, true)
  assert.equal(state[1].open, true)
  state[1].open = false
  assert.equal(reconcileWorkflowExpansion(state, failed, true, false)[1].open, true)
})

test('GitLab section occurrences reset expansion when their source identity changes', () => {
  const first = parse([start('same', 1760000000, 'Title', true), end('same')])
  const previous = reconcileWorkflowExpansion({}, first, false, false)
  previous[1].open = true
  const later = parse([start('same', 1760000004, 'Title', true), end('same', 1760000005)])
  assert.equal(reconcileWorkflowExpansion(previous, later, false, false)[1].open, false)
})

test('GitHub grouping and mixed native sections preserve provider boundaries', () => {
  const parsed = parse([
    '##[group]GitHub outer',
    start('gitlab', 1760000000, 'GitLab inner', true),
    'details',
    end('gitlab'),
    'outer details',
    '##[endgroup]',
  ])
  assert.equal(parsed.groups.get(1)?.provider, 'github')
  assert.equal(parsed.groups.get(1)?.count, 3)
  assert.equal(parsed.groups.get(1)?.durationSeconds, undefined)
  assert.equal(parsed.groups.get(2)?.provider, 'gitlab')
  assert.deepEqual(parsed.rows.find((row) => row.number === 5)?.parents, [1])
  assert.equal(reconcileWorkflowExpansion({}, parsed, false, false)[1].open, false)
  const unmatched = parse([
    start('only-gitlab'),
    '##[endgroup]',
    'still inside',
    end('only-gitlab'),
  ])
  assert.deepEqual(unmatched.rows.find((row) => row.number === 3)?.parents, [1])
})

test('GitLab excess nesting remains bounded and cannot close shallower duplicate IDs', () => {
  const parsed = parse([
    ...Array.from({ length: 1000 }, () => start('same')),
    'deep output',
    ...Array.from({ length: 1000 }, () => end('same')),
    'outside',
  ])
  assert.equal(parsed.groups.size, 32)
  assert.ok(parsed.rows.every((row) => row.parents.length <= 32))
  assert.ok([...parsed.groups.values()].every((group) => !group.unfinished))
  assert.deepEqual(parsed.rows.at(-1)?.parents, [])
})

test('GitLab search and paging retain section context within the existing DOM limit', () => {
  const parsed = parse([
    start('large', 1760000000, 'Large output', true),
    ...Array.from({ length: 2400 }, (_, i) => `needle ${i}`),
    end('large'),
  ])
  const page = workflowLogPage(parsed, 'needle', null)
  assert.equal(page.total, 2400)
  assert.equal(page.rows[0].number, 1)
  assert.ok(page.rows.length <= 1000)
  const earlier = workflowLogPage(parsed, 'needle', page.start)
  assert.equal(earlier.end, page.start)
  for (const row of page.rows)
    for (const id of row.parents) assert.ok(page.rows.some((parent) => parent.number === id))
})

test('GitLab expansion state keeps the existing retained-group bound', () => {
  const parsed = parse(
    Array.from({ length: 10001 }, (_, i) => [start(`section_${i}`), end(`section_${i}`)]).flat(),
  )
  assert.equal(Object.keys(reconcileWorkflowExpansion({}, parsed, false, false)).length, 10000)
})

test('step output assigns boundaries once and preserves all unassigned output', () => {
  const output = lines([
    '2026-09-30T00:00:00Z Before steps',
    '2026-09-30T00:00:01Z First step',
    '2026-09-30T00:00:02Z Second step boundary',
    'Untimestamped provider output',
    '2026-09-30T00:00:04Z After completed steps',
  ])
  const partition = workflowStepOutput(output, [
    { number: 1, started_at: '2026-09-30T00:00:01Z', completed_at: '2026-09-30T00:00:02Z' },
    { number: 2, started_at: '2026-09-30T00:00:02Z', completed_at: '2026-09-30T00:00:03Z' },
    { number: 3, started_at: null },
  ])
  assert.deepEqual(
    partition.byStep.get(1)?.map((line) => line.number),
    [2],
  )
  assert.deepEqual(
    partition.byStep.get(2)?.map((line) => line.number),
    [3],
  )
  assert.deepEqual(partition.byStep.get(3), [])
  assert.deepEqual(
    partition.unmatched.map((line) => line.number),
    [1, 4, 5],
  )
  assert.equal(
    [...partition.byStep.values()].flat().length + partition.unmatched.length,
    output.length,
  )
})
