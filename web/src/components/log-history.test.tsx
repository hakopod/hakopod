import assert from 'node:assert/strict'
import test from 'node:test'
import { renderToStaticMarkup } from 'react-dom/server'
import { PastPods, CutMark, HistoryStatus, HistoryUnavailable } from './log-history'
import { Form } from './app-log-settings'
import { SelectField } from './ui/select'
import {
  podOptions,
  timeOptions,
  validateCustom,
  validateLogSettings,
  type AppLogSource,
} from '../lib/app-logs'

const now = Date.parse('2026-10-27T12:00:00Z')
const pod = (over: Partial<AppLogSource>): AppLogSource => ({
  pod_uid: 'u1',
  pod_name: 'web-6d9f-abcde',
  service: 'web',
  revision: 3,
  node: 'n',
  source: 'file',
  started_at: '2026-10-27T01:00:00Z',
  ended_at: null,
  end_reason: '',
  restarts: 0,
  gap_seconds: 0,
  current: true,
  ...over,
})

test('pod options list current pods before past pods, with plain reasons', () => {
  const options = podOptions(
    [
      pod({
        pod_uid: 'old',
        pod_name: 'web-1',
        current: false,
        ended_at: '2026-10-26T12:00:00Z',
        end_reason: 'evicted',
      }),
      pod({
        pod_uid: 'oom',
        pod_name: 'web-6d9f-abcde',
        current: false,
        ended_at: '2026-10-27T10:00:00Z',
        end_reason: 'oom_killed',
      }),
      pod({ pod_uid: 'cur', pod_name: 'web-7c1a-xyz' }),
    ],
    now,
  )
  assert.deepEqual(
    options.map((o) => o.label),
    [
      'All pods',
      'web-7c1a-xyz · running',
      'web-6d9f-abcde · ended 2h ago · OOM killed',
      'web-1 · ended 1d ago · Evicted',
    ],
  )
  const html = renderToStaticMarkup(
    <SelectField label="Pod" value="oom" onValueChange={() => {}} options={options} />,
  )
  assert.match(html, /OOM killed/)
})

test('gap warning is visible with pod, seconds and note', () => {
  const html = renderToStaticMarkup(
    <HistoryStatus
      now={now}
      history={{
        available: true,
        message: '',
        collected_through: '2026-10-27T11:00:00Z',
        scan_limited: true,
        retention_days: 7,
        gaps: [
          {
            pod_uid: 'u',
            pod_name: 'web-1',
            seconds: 5,
            note: 'Log file rotated before Hakopod read it.',
          },
        ],
      }}
    />,
  )
  assert.match(html, /Logs collected up to 1h ago/)
  assert.match(html, /Not all stored lines were searched\. Narrow the time range or filters\./)
  assert.match(html, /Some output may be missing:/)
  assert.match(html, /web-1: about 5 seconds missing\. Log file rotated before Hakopod read it\./)
})

test('past pods table wraps names, keeps case and marks the selected row', () => {
  const html = renderToStaticMarkup(
    <PastPods
      selected="oom"
      onSelect={() => {}}
      items={[
        pod({
          pod_uid: 'oom',
          current: false,
          ended_at: '2026-10-27T10:00:00Z',
          end_reason: 'oom_killed',
        }),
      ]}
    />,
  )
  assert.match(html, /role="region"/)
  assert.match(html, /bg-\[var\(--hairline\)\] font-medium/)
  assert.doesNotMatch(html, /aria-selected/)
  assert.doesNotMatch(html, /class="[^"]*\bbutton\b/)
})

test('unavailable note shows the server message once, with upgrade command wrapped', () => {
  const html = renderToStaticMarkup(
    <HistoryUnavailable message="Log history is not available on this installation. Run installer.sh --upgrade." />,
  )
  assert.equal(html.split('not available').length, 2)
  assert.match(html, /<code class="whitespace-nowrap">installer\.sh --upgrade<\/code>/)
  assert.match(
    renderToStaticMarkup(<HistoryUnavailable />),
    /Log history is not available on this installation\./,
  )
  const none = renderToStaticMarkup(
    <HistoryStatus
      history={{
        available: true,
        message: '',
        collected_through: null,
        scan_limited: false,
        retention_days: 7,
        gaps: [],
      }}
    />,
  )
  assert.match(none, /No logs collected yet\./)
})

test('unavailable fallback note and truncated mark', () => {
  assert.match(
    renderToStaticMarkup(<HistoryUnavailable message="The log directory is not writable." />),
    /The log directory is not writable\./,
  )
  assert.match(renderToStaticMarkup(<CutMark truncated />), /\(line cut\)/)
  assert.equal(renderToStaticMarkup(<CutMark />), '')
})

test('time options hide ranges beyond retention and validate custom ranges', () => {
  assert.deepEqual(
    timeOptions(3, true).map((o) => o.label),
    ['Last 15 minutes', 'Last hour', 'Last 6 hours', 'Last 24 hours', 'Last 3 days', 'Custom'],
  )
  assert.equal(timeOptions(7, true).length, 7)
  assert.equal(timeOptions(7, false).length, 4)
  assert.match(validateCustom('2026-10-27T10:00', '2026-10-27T09:00', 7, now), /after the start/)
  assert.match(validateCustom('2026-10-01T10:00', '2026-10-27T09:00', 7, now), /7-day retention/)
  assert.equal(validateCustom('2026-10-26T10:00', '2026-10-27T09:00', 7, now), '')
})

test('settings validation messages and retention help', () => {
  assert.match(validateLogSettings('0', '512').retention, /1 to 30/)
  assert.match(validateLogSettings('7', '32').size, /64 to 4096/)
  assert.deepEqual(validateLogSettings('7', '512'), { retention: '', size: '' })
  const html = renderToStaticMarkup(
    <Form initial={{ enabled: true, retention_days: 99, max_mib_per_app: 512, revision: 1 }} />,
  )
  assert.match(html, /Retention must be a whole number of days from 1 to 30\./)
  assert.match(html, /disabled/)
  assert.match(html, /aria-describedby="app-log-days-error"/)
  assert.match(html, /text-destructive/)
})
