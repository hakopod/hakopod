import { Note } from './shared'
import { ago, endReason, unavailablePrefix, type AppLogSource } from '../lib/app-logs'
import { timestamp } from '../lib/api'
import type { components } from '../lib/api.generated'

type History = components['schemas']['LogHistory']

export function HistoryUnavailable({ message }: { message?: string }) {
  const text = message?.trim() || unavailablePrefix
  const [before, ...rest] = text.split('installer.sh --upgrade')
  return (
    <Note>
      {before}
      {rest.length > 0 && (
        <>
          <code className="whitespace-nowrap">installer.sh --upgrade</code>
          {rest.join('installer.sh --upgrade')}
        </>
      )}{' '}
      Showing live logs from current pods.
    </Note>
  )
}

export function HistoryStatus({ history, now }: { history: History; now?: number }) {
  return (
    <div className="grid gap-2">
      <p className="m-0 text-sm text-muted-foreground [overflow-wrap:anywhere]">
        {!history.collected_through
          ? 'No logs collected yet.'
          : ago(history.collected_through, now) === 'just now'
            ? 'Logs are up to date.'
            : `Logs collected up to ${ago(history.collected_through, now)}.`}
        {history.scan_limited &&
          ' Not all stored lines were searched. Narrow the time range or filters.'}
      </p>
      {history.gaps.length > 0 && (
        <Note>
          <div className="text-foreground">
            <strong>Some output may be missing:</strong>
            <ul className="m-0 mt-1 grid gap-1 pl-4">
              {history.gaps.map((gap, index) => (
                <li key={`${gap.pod_uid}-${index}`}>
                  {gap.pod_name}: about {gap.seconds} seconds missing. {gap.note}
                </li>
              ))}
            </ul>
          </div>
        </Note>
      )}
    </div>
  )
}

export const CutMark = ({ truncated }: { truncated?: boolean }) =>
  truncated ? <small className="text-muted-foreground"> (line cut)</small> : null

export function PastPods({
  items,
  selected,
  onSelect,
}: {
  items: readonly AppLogSource[]
  selected: string
  onSelect: (uid: string) => void
}) {
  return (
    <div className="overflow-x-auto" role="region" aria-label="Past pods" tabIndex={0}>
      <table className="w-full min-w-[640px] table-fixed text-left text-sm">
        <thead>
          <tr>
            {['Pod', 'Service', 'Revision', 'Started', 'Ended', 'Reason', 'Gap'].map((h) => (
              <th
                key={h}
                scope="col"
                className={`px-2 py-1 font-medium text-muted-foreground ${
                  h === 'Pod' ? 'w-[26%]' : h === 'Gap' ? 'w-[20%] min-w-[120px]' : ''
                }`}
              >
                {h}
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {items.map((item) => {
            const on = selected === item.pod_uid
            return (
              <tr key={item.pod_uid} className={on ? 'bg-[var(--hairline)] font-medium' : ''}>
                <td className="px-2 py-1 [overflow-wrap:anywhere]">
                  <button
                    type="button"
                    aria-pressed={on}
                    aria-label={`Show logs for ${item.pod_name}`}
                    className="m-0 cursor-pointer border-0 bg-transparent p-0 text-left normal-case [overflow-wrap:anywhere] [word-break:break-word] underline-offset-2 hover:underline focus-visible:outline-2"
                    onClick={() => onSelect(on ? '' : item.pod_uid)}
                  >
                    {item.pod_name}
                  </button>
                </td>
                <td className="px-2 py-1 [overflow-wrap:anywhere]">{item.service}</td>
                <td className="px-2 py-1">{item.revision || ''}</td>
                <td className="whitespace-nowrap px-2 py-1">{timestamp(item.started_at)}</td>
                <td className="whitespace-nowrap px-2 py-1">
                  {item.ended_at ? timestamp(item.ended_at) : 'running'}
                </td>
                <td className="px-2 py-1">{endReason(item.end_reason)}</td>
                <td className="px-2 py-1 [overflow-wrap:anywhere]">
                  {item.gap_seconds > 0
                    ? `${item.gap_seconds}s${item.gap_note ? ` · ${item.gap_note}` : ''}`
                    : ''}
                </td>
              </tr>
            )
          })}
        </tbody>
      </table>
    </div>
  )
}
