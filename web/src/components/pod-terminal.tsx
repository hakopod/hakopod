import { readTerminalEvents } from '../lib/terminal-stream'
import { useEffect, useRef, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { Badge } from '@hakopod/ui'
import type { Terminal } from '@xterm/xterm'
import '@xterm/xterm/css/xterm.css'
import { client, unwrap } from '../lib/client'
import { message, timestamp } from '../lib/api'
import { useScope } from '../lib/scope'
import { Button } from './ui/button'
import { Icon } from './icons'
import { Empty, Note } from './shared'
import type { components } from '../lib/api.generated'
const presets = {
  sh: ['/bin/sh'],
  bash: ['/bin/bash'],
  psql: ['psql'],
  redis: ['redis-cli'],
  valkey: ['valkey-cli'],
  mysql: ['mysql'],
  mongo: ['mongosh'],
} as const
type Session = components['schemas']['TerminalSession']
export default function PodTerminal({
  applicationId,
  services,
  initialService,
  initialPod,
}: {
  applicationId: string
  services: string[]
  initialService?: string
  initialPod?: string
}) {
  const scope = useScope()
  const [service, setService] = useState(initialService || services[0] || '')
  const [pod, setPod] = useState(initialPod || '')
  const [container, setContainer] = useState('app')
  const [preset, setPreset] = useState('sh')
  const [command, setCommand] = useState('["/bin/sh"]')
  const [session, setSession] = useState<Session | null>(null)
  const [state, setState] = useState('Disconnected')
  const [busy, setBusy] = useState(false)
  const [connected, setConnected] = useState(false)
  const [error, setError] = useState('')
  const element = useRef<HTMLDivElement>(null)
  const active = useRef<Session | null>(null)
  const controller = useRef<AbortController | null>(null)
  const terminal = useRef<Terminal | null>(null)
  const dispose = useRef<(() => void) | null>(null)
  const alive = useRef(true)
  const runtime = useQuery({
    queryKey: ['terminal-pods', applicationId, service],
    queryFn: ({ signal }) =>
      unwrap(
        client.GET('/applications/{id}/services/{service}/runtime', {
          signal,
          params: { path: { id: applicationId, service } },
        }),
      ),
    enabled: Boolean(service) && scope.can('deployments:write'),
    gcTime: 0,
    staleTime: 10000,
  })
  const chosenPod = pod || runtime.data?.pods.find((item) => item.phase === 'Running')?.name || ''
  const base = `/api/applications/${encodeURIComponent(applicationId)}/services/${encodeURIComponent(service)}/terminal`
  const close = (reason = 'Disconnected') => {
    const current = active.current
    active.current = null
    controller.current?.abort()
    dispose.current?.()
    dispose.current = null
    if (terminal.current) terminal.current.options.disableStdin = true
    if (alive.current) {
      setConnected(false)
      setState(reason)
    }
    if (current)
      void fetch(`${base}/${encodeURIComponent(current.id)}`, {
        method: 'DELETE',
        keepalive: true,
      }).catch(() => undefined)
  }
  useEffect(() => {
    alive.current = true
    return () => {
      alive.current = false
      close()
      terminal.current?.dispose()
      terminal.current = null
    }
  }, [applicationId, service])
  async function connect() {
    if (!element.current || !chosenPod || busy) return
    let argv: string[]
    try {
      const parsed: unknown = JSON.parse(command)
      if (
        !Array.isArray(parsed) ||
        parsed.length === 0 ||
        parsed.length > 16 ||
        parsed.some((item) => typeof item !== 'string')
      )
        throw new Error()
      argv = parsed as string[]
    } catch {
      setError('Command must be a JSON array of executable and arguments.')
      return
    }
    close()
    setBusy(true)
    setError('')
    setState('Loading terminal…')
    try {
      const [{ Terminal: XTerm }, { FitAddon }] = await Promise.all([
        import('@xterm/xterm'),
        import('@xterm/addon-fit'),
      ])
      if (!alive.current || !element.current) return
      terminal.current?.dispose()
      const css = getComputedStyle(document.documentElement)
      const term = new XTerm({
        cursorBlink: false,
        scrollback: 500,
        fontSize: 13,
        lineHeight: 1.25,
        fontFamily: css.getPropertyValue('--font-mono').trim(),
        disableStdin: true,
        theme: {
          background: css.getPropertyValue('--bg').trim(),
          foreground: css.getPropertyValue('--text').trim(),
          cursor: css.getPropertyValue('--accent').trim(),
          selectionBackground: '#60706555',
        },
      })
      const fit = new FitAddon()
      term.loadAddon(fit)
      term.open(element.current)
      fit.fit()
      terminal.current = term
      setState('Connecting to pod…')
      const created = await unwrap(
        client.POST('/applications/{id}/services/{service}/terminal', {
          params: { path: { id: applicationId, service } },
          body: {
            pod: chosenPod,
            container,
            command: argv,
            cols: Math.min(400, Math.max(20, term.cols)),
            rows: Math.min(200, Math.max(5, term.rows)),
          },
        }),
      )
      if (!alive.current) {
        void fetch(`${base}/${created.id}`, { method: 'DELETE', keepalive: true })
        return
      }
      active.current = created
      setSession(created)
      const abort = new AbortController()
      controller.current = abort
      const response = await fetch(`${base}/${encodeURIComponent(created.id)}/output`, {
        signal: abort.signal,
        cache: 'no-store',
      })
      if (!response.ok || !response.body)
        throw new Error(`Terminal output could not attach (${response.status}).`)
      term.options.disableStdin = false
      term.focus()
      setConnected(true)
      setState('Connected')
      setBusy(false)
      let queued = new Uint8Array(0)
      let sending = false
      const input = async (body: { data?: string; cols?: number; rows?: number }) => {
        const result = await fetch(`${base}/${encodeURIComponent(created.id)}/input`, {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify(body),
          signal: abort.signal,
        })
        if (!result.ok) throw new Error(`Terminal input was rejected (${result.status}).`)
      }
      const flush = async () => {
        if (sending) return
        sending = true
        try {
          while (queued.length && !abort.signal.aborted) {
            const chunk = queued.slice(0, 4096)
            queued = queued.slice(chunk.length)
            await input({ data: btoa(String.fromCharCode(...chunk)) })
          }
        } catch (err) {
          if (!abort.signal.aborted && alive.current) {
            setError(message(err))
            close('Input connection closed')
          }
        } finally {
          sending = false
        }
      }
      const dataSubscription = term.onData((data) => {
        const bytes = new TextEncoder().encode(data)
        if (queued.length + bytes.length > 16 * 1024) {
          setError('Input buffer is full. Paste at most 16 KiB at a time.')
          return
        }
        const next = new Uint8Array(queued.length + bytes.length)
        next.set(queued)
        next.set(bytes, queued.length)
        queued = next
        void flush()
      })
      let resizeTimer: ReturnType<typeof setTimeout> | undefined
      const resize = new ResizeObserver(() => {
        clearTimeout(resizeTimer)
        resizeTimer = setTimeout(() => {
          if (abort.signal.aborted) return
          fit.fit()
          void input({
            cols: Math.min(400, Math.max(20, term.cols)),
            rows: Math.min(200, Math.max(5, term.rows)),
          }).catch(() => undefined)
        }, 150)
      })
      resize.observe(element.current)
      dispose.current = () => {
        dataSubscription.dispose()
        resize.disconnect()
        clearTimeout(resizeTimer)
        queued = new Uint8Array(0)
      }
      let ended = false
      for await (const event of readTerminalEvents(response.body, abort.signal)) {
        if (event.type === 'output')
          await new Promise<void>((resolve) => term.write(event.data, resolve))
        else {
          ended = true
          close(`Exited ${event.code ?? '—'}${event.message ? ` · ${event.message}` : ''}`)
        }
      }
      if (!ended && active.current?.id === created.id) close('Output stream closed')
    } catch (err) {
      if (alive.current && !(err instanceof DOMException && err.name === 'AbortError')) {
        setError(message(err))
        close('Connection unavailable')
      }
    } finally {
      if (alive.current) setBusy(false)
    }
  }
  if (!scope.can('deployments:write'))
    return (
      <Empty
        icon="lock"
        title="Terminal requires deployment access"
        description="Ask a project administrator for permission to operate this application."
      />
    )
  return (
    <section className="terminal-panel">
      <div className="explorer-heading">
        <div>
          <Icon name="terminal" size={18} />
          <h2>Pod terminal</h2>
          <Badge tone={connected ? 'success' : 'neutral'}>{state}</Badge>
        </div>
        {connected && (
          <Button size="sm" variant="danger" onClick={() => close()}>
            Disconnect
          </Button>
        )}
      </div>
      <div className="terminal-controls">
        <label>
          Service
          <select
            disabled={connected || busy}
            value={service}
            onChange={(e) => {
              setService(e.target.value)
              setPod('')
            }}
          >
            {services.map((name) => (
              <option key={name}>{name}</option>
            ))}
          </select>
        </label>
        <label>
          Pod
          <select
            disabled={connected || busy}
            value={chosenPod}
            onChange={(e) => setPod(e.target.value)}
          >
            <option value="">Choose running pod</option>
            {runtime.data?.pods.map((item) => (
              <option key={item.name} value={item.name}>
                {item.name} · {item.phase}
              </option>
            ))}
          </select>
        </label>
        <label>
          Container
          <select
            disabled={connected || busy}
            value={container}
            onChange={(e) => setContainer(e.target.value)}
          >
            {(
              runtime.data?.pods
                .find((item) => item.name === chosenPod)
                ?.containers.map((item) => item.name) || ['app']
            ).map((name) => (
              <option key={name}>{name}</option>
            ))}
          </select>
        </label>
        <label>
          Client
          <select
            disabled={connected || busy}
            value={preset}
            onChange={(e) => {
              setPreset(e.target.value)
              if (e.target.value !== 'custom')
                setCommand(JSON.stringify(presets[e.target.value as keyof typeof presets]))
            }}
          >
            <option value="sh">Shell · sh</option>
            <option value="bash">Shell · bash</option>
            <option value="psql">PostgreSQL · psql</option>
            <option value="redis">Redis · redis-cli</option>
            <option value="valkey">Valkey · valkey-cli</option>
            <option value="mysql">MySQL · mysql</option>
            <option value="mongo">MongoDB · mongosh</option>
            <option value="custom">Custom command</option>
          </select>
        </label>
      </div>
      {preset === 'custom' && (
        <label className="terminal-command">
          Command and arguments
          <input
            className="mono"
            disabled={connected || busy}
            value={command}
            onChange={(e) => setCommand(e.target.value)}
            maxLength={2048}
          />
        </label>
      )}
      <div className="terminal-connect">
        <p>
          {session && connected
            ? `${session.pod} · Ends by ${timestamp(session.expires_at)}`
            : 'Connect to an existing container. Database clients use the container’s environment and must be installed in its image.'}
        </p>
        {!connected && (
          <Button variant="primary" disabled={busy || !chosenPod} onClick={() => void connect()}>
            <Icon name="terminal" size={14} />
            {busy ? 'Connecting…' : 'Connect'}
          </Button>
        )}
      </div>
      {error && (
        <div className="inline-error" role="alert">
          {error}
        </div>
      )}
      {runtime.error && (
        <Note>Pod discovery is unavailable. Refresh the service before connecting.</Note>
      )}
      <div className="terminal-surface" ref={element} aria-label="Interactive pod terminal" />
      <div className="log-footer">
        <span>
          {connected ? 'Commands run inside the selected container' : 'Terminal is not connected'}
        </span>
        <span>500-line scrollback · 10-minute maximum · 2-minute idle timeout</span>
      </div>
      <Note>
        Leaving this terminal tab closes its session. A database connection requires its normal
        database permissions.
      </Note>
    </section>
  )
}
