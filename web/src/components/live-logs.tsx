import { Input } from './ui/input'
import { SelectField } from './ui/select'
import { useEffect, useRef, useState } from 'react'
import { Icon } from './icons'
import { Button } from './ui/button'
import { Copy, Empty, Note } from './shared'
import { message } from '../lib/api'

export default function LiveLogs({
  applicationId,
  services,
  initialService,
}: {
  applicationId: string
  services: string[]
  initialService?: string
}) {
  const [service, setService] = useState(initialService || services[0] || '')
  const [wrap, setWrap] = useState(false)
  const [follow, setFollow] = useState(false)
  const [visible, setVisible] = useState(true)
  const [restart, setRestart] = useState(0)
  const [text, setText] = useState('')
  const [state, setState] = useState('Connecting')
  const [error, setError] = useState('')
  const end = useRef<HTMLDivElement>(null)
  useEffect(() => {
    const change = () => setVisible(!document.hidden)
    document.addEventListener('visibilitychange', change)
    change()
    return () => document.removeEventListener('visibilitychange', change)
  }, [])
  useEffect(() => {
    if (!service || !visible) return
    const controller = new AbortController()
    let alive = true
    let flush: ReturnType<typeof setInterval> | undefined
    let backoff: ReturnType<typeof setTimeout> | undefined
    let buffer = ''
    let dirty = false
    setText('')
    setError('')
    const publish = () => {
      if (alive && dirty) {
        setText(buffer)
        dirty = false
      }
    }
    async function connect(attempt = 0) {
      setState(attempt ? `Reconnecting (${attempt}/3)` : 'Connecting')
      try {
        const response = await fetch(
          `/api/applications/${encodeURIComponent(applicationId)}/logs?service=${encodeURIComponent(service)}&tail=100&follow=${follow}`,
          { signal: controller.signal, cache: 'no-store' },
        )
        if (!response.ok) {
          const payload = await response.json().catch(() => null)
          throw new Error(payload?.error?.message || `Logs unavailable (${response.status}).`)
        }
        if (!response.body) throw new Error('The API returned no log stream.')
        setState(follow ? 'Live' : 'Loading recent logs')
        const reader = response.body.getReader()
        const decoder = new TextDecoder()
        try {
          while (alive) {
            const result = await reader.read()
            if (result.done) break
            // UTF-16 uses at most two bytes per code unit: cap text at 256 KiB.
            buffer = (buffer + decoder.decode(result.value, { stream: true })).slice(-128 * 1024)
            // Scan at most 1,000 delimiters without allocating an array of lines.
            let offset = buffer.length
            for (let line = 0; line < 1000 && offset > 0; line++) {
              offset = buffer.lastIndexOf('\n', offset - 1)
              if (offset < 0) break
              if (line === 999) buffer = buffer.slice(offset + 1)
            }
            dirty = true
          }
        } finally {
          await reader.cancel().catch(() => undefined)
          reader.releaseLock()
        }
        publish()
        if (!alive) return
        if (follow && attempt < 3) {
          setState('Reconnecting')
          backoff = setTimeout(
            () => void connect(attempt + 1),
            Math.min(1000 * 2 ** attempt, 10000),
          )
        } else setState(follow ? 'Stream ended · reconnect to continue' : 'Recent logs')
      } catch (err) {
        if (alive && !controller.signal.aborted) {
          setError(message(err))
          setState('Unavailable')
        }
      }
    }
    flush = setInterval(publish, 250)
    void connect()
    return () => {
      alive = false
      controller.abort()
      clearInterval(flush)
      clearTimeout(backoff)
    }
  }, [applicationId, service, follow, visible, restart])
  useEffect(() => {
    if (follow) end.current?.scrollIntoView({ block: 'nearest' })
  }, [text, follow])
  const download = () => {
    const url = URL.createObjectURL(new Blob([text], { type: 'text/plain;charset=utf-8' }))
    const link = document.createElement('a')
    link.href = url
    link.download = `hakopod-${service}-logs.txt`
    link.click()
    URL.revokeObjectURL(url)
  }
  if (!services.length)
    return (
      <Empty
        icon="terminal"
        title="No services to stream"
        description="Deploy a service to view its container output."
      />
    )
  return (
    <div className="ops-live-logs">
      <div className="logs-toolbar">
        <div className="inline-field">
          <Icon name="box" size={15} />
          <SelectField
            label="Log service"
            compact
            value={service}
            onValueChange={setService}
            options={services.map((name) => ({ value: name, label: name }))}
          />
        </div>
        <span className={`log-connection ${state === 'Live' && visible ? 'log-connected' : ''}`}>
          <span className="status-dot" />
          {visible ? state : 'Paused while tab is hidden'}
        </span>
        <div className="form-spacer" />
        <label className="checkbox-row">
          <Input
            type="checkbox"
            checked={wrap}
            onChange={(event) => setWrap(event.target.checked)}
          />
          Wrap
        </label>
        <Copy value={text} label="Copy" />
        <Button size="sm" variant="ghost" disabled={!text} onClick={download}>
          Download
        </Button>
        <Button variant="primary" size="sm" onClick={() => setFollow((value) => !value)}>
          <Icon name={follow ? 'pause' : 'play'} size={14} />
          {follow ? 'Pause live' : 'Follow live'}
        </Button>
        <Button
          size="icon"
          aria-label="Reload logs"
          onClick={() => setRestart((value) => value + 1)}
        >
          <Icon name="refresh" size={15} />
        </Button>
      </div>
      <div className={`log-window ${wrap ? 'ops-log-wrap' : ''}`} role="log" aria-live="off">
        {error ? (
          <div className="log-error">
            <Icon name="alert" size={17} />
            {error}
          </div>
        ) : text ? (
          <pre>{text}</pre>
        ) : (
          <div className="log-placeholder">
            <Icon name="terminal" size={22} />
            <span>
              {state === 'Connecting' || state === 'Loading recent logs'
                ? 'Waiting for container output…'
                : 'No log lines returned for this service.'}
            </span>
          </div>
        )}
        <div ref={end} />
      </div>
      <div className="log-footer">
        <span>Ephemeral container logs</span>
        <span>Last 1,000 lines · 256 KiB maximum · Starts with 100 lines</span>
      </div>
      <Note>
        Logs are read directly from Kubernetes and are not retained by Hakopod. Application output
        may contain sensitive data.
      </Note>
    </div>
  )
}
