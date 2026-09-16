import { editionFetch } from '../lib/client-edition'
import { Input } from './ui/input'
import { SelectField } from './ui/select'
import { readTerminalEvents } from '../lib/terminal-stream'
import { useEffect, useRef, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { Badge } from './ui/surfaces'
import type { Terminal } from '@xterm/xterm'
import '@xterm/xterm/css/xterm.css'
import { client, unwrap } from '../lib/client'
import { message, timestamp } from '../lib/api'
import { useScope, canOpenHostTerminal } from '../lib/scope'
import { Button } from './ui/button'
import { Icon } from './icons'
import { Empty, Note, RequestError } from './shared'
const presets = {
  sh: ['/bin/sh'],
  bash: ['/bin/bash'],
  psql: ['psql'],
  redis: ['redis-cli'],
  valkey: ['valkey-cli'],
  mysql: ['mysql'],
  mongo: ['mongosh'],
} as const
type Session = { id: string; expires_at: string; pod?: string; container?: string; node?: string }
export default function PodTerminal({
  applicationId = '',
  services = [],
  initialService,
  initialPod,
  hostNode,
}: {
  applicationId?: string
  services?: string[]
  hostNode?: string
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
  const [copied, setCopied] = useState(false)
  const element = useRef<HTMLDivElement>(null)
  const active = useRef<Session | null>(null)
  const controller = useRef<AbortController | null>(null)
  const terminal = useRef<Terminal | null>(null)
  const dispose = useRef<(() => void) | null>(null)
  const alive = useRef(true)
  const attempt = useRef(0)
  const allowed = hostNode
    ? canOpenHostTerminal(scope.identity, hostNode)
    : scope.can('deployments:write')
  const runtime = useQuery({
    queryKey: ['terminal-pods', applicationId, service],
    queryFn: ({ signal }) =>
      unwrap(
        client.GET('/applications/{id}/services/{service}/runtime', {
          signal,
          params: { path: { id: applicationId, service } },
        }),
      ),
    enabled: !hostNode && Boolean(service) && allowed,
    gcTime: 0,
    staleTime: 10000,
  })
  const chosenPod = pod || runtime.data?.pods.find((item) => item.phase === 'Running')?.name || ''
  const base = hostNode
    ? `/api/nodes/${encodeURIComponent(hostNode)}/terminal`
    : `/api/applications/${encodeURIComponent(applicationId)}/services/${encodeURIComponent(service)}/terminal`
  const close = (reason = 'Disconnected') => {
    attempt.current += 1
    const current = active.current
    active.current = null
    controller.current?.abort()
    dispose.current?.()
    dispose.current = null
    if (terminal.current) terminal.current.options.disableStdin = true
    if (alive.current) {
      setBusy(false)
      setConnected(false)
      setState(reason)
    }
    if (current)
      void editionFetch(`${base}/${encodeURIComponent(current.id)}`, {
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
  }, [applicationId, service, hostNode])
  useEffect(() => {
    const update = () => {
      if (!terminal.current) return
      const css = getComputedStyle(document.documentElement)
      terminal.current.options.theme = {
        background: css.getPropertyValue('--background').trim(),
        foreground: css.getPropertyValue('--foreground').trim(),
        cursor: css.getPropertyValue('--action').trim(),
        selectionBackground: '#60706555',
      }
    }
    const observer = new MutationObserver(update)
    observer.observe(document.documentElement, {
      attributes: true,
      attributeFilter: ['class', 'data-theme'],
    })
    return () => observer.disconnect()
  }, [])
  const readOutput = () => {
    const buffer = terminal.current?.buffer.active
    if (!buffer) return ''
    const lines: string[] = []
    for (let i = Math.max(0, buffer.length - 500); i < buffer.length; i++)
      lines.push(buffer.getLine(i)?.translateToString(true) || '')
    return lines.join('\n')
  }
  const download = () => {
    const url = URL.createObjectURL(new Blob([readOutput()], { type: 'text/plain;charset=utf-8' }))
    const link = document.createElement('a')
    link.href = url
    link.download = 'hakopod-terminal.txt'
    link.click()
    URL.revokeObjectURL(url)
  }
  async function connect() {
    if (!element.current || (!hostNode && !chosenPod) || busy || !allowed) return
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
    const generation = attempt.current
    setBusy(true)
    setError('')
    setCopied(false)
    setState('Loading terminal…')
    try {
      const [{ Terminal: XTerm }, { FitAddon }] = await Promise.all([
        import('@xterm/xterm'),
        import('@xterm/addon-fit'),
      ])
      if (!alive.current || generation !== attempt.current || !element.current) return
      terminal.current?.dispose()
      const css = getComputedStyle(document.documentElement)
      const term = new XTerm({
        cursorBlink: false,
        scrollback: 500,
        fontSize: 13,
        lineHeight: 1.8,
        fontFamily: css.getPropertyValue('--font-mono').trim(),
        disableStdin: true,
        theme: {
          background: css.getPropertyValue('--background').trim(),
          foreground: css.getPropertyValue('--foreground').trim(),
          cursor: css.getPropertyValue('--action').trim(),
          selectionBackground: '#60706555',
        },
      })
      const fit = new FitAddon()
      term.loadAddon(fit)
      term.open(element.current)
      fit.fit()
      terminal.current = term
      setState(hostNode ? 'Connecting to Linux node…' : 'Connecting to pod…')
      const created = hostNode
        ? await unwrap(
            client.POST('/nodes/{node}/terminal', {
              params: { path: { node: hostNode } },
              body: {
                cols: Math.min(400, Math.max(20, term.cols)),
                rows: Math.min(200, Math.max(5, term.rows)),
              },
            }),
          )
        : await unwrap(
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
      if (!alive.current || generation !== attempt.current) {
        void editionFetch(`${base}/${encodeURIComponent(created.id)}`, {
          method: 'DELETE',
          keepalive: true,
        }).catch(() => undefined)
        return
      }
      active.current = created
      setSession(created)
      const abort = new AbortController()
      controller.current = abort
      const response = await editionFetch(`${base}/${encodeURIComponent(created.id)}/output`, {
        signal: abort.signal,
        cache: 'no-store',
      })
      if (!alive.current || generation !== attempt.current) {
        await response.body?.cancel()
        return
      }
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
        const result = await editionFetch(`${base}/${encodeURIComponent(created.id)}/input`, {
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
      if (
        alive.current &&
        generation === attempt.current &&
        !(err instanceof DOMException && err.name === 'AbortError')
      ) {
        setError(message(err))
        close('Connection unavailable')
      }
    } finally {
      if (alive.current && generation === attempt.current) setBusy(false)
    }
  }
  if (!allowed)
    return (
      <Empty
        icon="lock"
        title={hostNode ? 'Host terminal access required' : 'Terminal requires deployment access'}
        description={
          hostNode
            ? 'The super admin must grant terminal authority for this node.'
            : 'Ask a project administrator for permission to operate this application.'
        }
      />
    )
  return (
    <section className="terminal-panel ops-terminal">
      <div className="explorer-heading">
        <div className="hako-section-heading-title">
          <h2>{hostNode ? `Host terminal · ${hostNode}` : 'Pod terminal'}</h2>
          <Badge tone={connected ? 'success' : 'neutral'}>{state}</Badge>
        </div>
        <div className="toolbar-actions">
          <Button
            variant="ghost"
            size="sm"
            disabled={!session}
            onClick={() => {
              void navigator.clipboard
                .writeText(terminal.current?.getSelection() || readOutput())
                .then(
                  () => setCopied(true),
                  () => setError('Clipboard unavailable. Download the terminal output instead.'),
                )
            }}
          >
            {copied ? 'Copied' : 'Copy'}
          </Button>
          <Button variant="ghost" size="sm" disabled={!session} onClick={download}>
            Download
          </Button>
          {connected && (
            <Button size="sm" variant="danger" onClick={() => close()}>
              Disconnect
            </Button>
          )}
        </div>
      </div>
      {!hostNode && (
        <div className="terminal-controls">
          <label>
            Service
            <SelectField
              label="Service"
              disabled={connected || busy}
              value={service}
              onValueChange={(value) => {
                setService(value)
                setPod('')
              }}
              options={services.map((name) => ({ value: name, label: name }))}
            />
          </label>
          <label>
            Pod
            <SelectField
              label="Pod"
              disabled={connected || busy}
              value={chosenPod}
              onValueChange={setPod}
              options={[
                { value: '', label: 'Choose running pod' },
                ...(runtime.data?.pods || []).map((item) => ({
                  value: item.name,
                  label: `${item.name} · ${item.phase}`,
                })),
              ]}
            />
          </label>
          <label>
            Container
            <SelectField
              label="Container"
              disabled={connected || busy}
              value={container}
              onValueChange={setContainer}
              options={(
                runtime.data?.pods
                  .find((item) => item.name === chosenPod)
                  ?.containers.map((item) => item.name) || ['app']
              ).map((name) => ({ value: name, label: name }))}
            />
          </label>
          <label>
            Client
            <SelectField
              label="Client"
              disabled={connected || busy}
              value={preset}
              onValueChange={(value) => {
                setPreset(value)
                if (value !== 'custom')
                  setCommand(JSON.stringify(presets[value as keyof typeof presets]))
              }}
              options={[
                { value: 'sh', label: 'Shell · sh' },
                { value: 'bash', label: 'Shell · bash' },
                { value: 'psql', label: 'PostgreSQL · psql' },
                { value: 'redis', label: 'Redis · redis-cli' },
                { value: 'valkey', label: 'Valkey · valkey-cli' },
                { value: 'mysql', label: 'MySQL · mysql' },
                { value: 'mongo', label: 'MongoDB · mongosh' },
                { value: 'custom', label: 'Custom command' },
              ]}
            />
          </label>
        </div>
      )}
      {!hostNode && preset === 'custom' && (
        <label className="terminal-command">
          Command and arguments
          <Input
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
            ? `${session.node || session.pod} · Ends by ${timestamp(session.expires_at)}`
            : hostNode
              ? 'This is a root shell on the selected Linux node. Commands can affect every workload and its data on that node.'
              : 'Connect to an existing container. Database clients use the container’s environment and must be installed in its image.'}
        </p>
        {!connected && (
          <Button
            variant="primary"
            disabled={busy || (!hostNode && !chosenPod)}
            onClick={() => void connect()}
          >
            <Icon name="terminal" size={14} />
            {busy ? 'Connecting…' : session ? 'Reconnect' : 'Connect'}
          </Button>
        )}
      </div>
      {error && <RequestError error={error} />}
      {runtime.error && (
        <Note>Pod discovery is unavailable. Refresh the service before connecting.</Note>
      )}
      {session && (
        <div className="ops-terminal-session">
          <Icon name="terminal" size={14} />
          <code>{session.node || session.pod}</code>
          <span>{connected ? 'Connected' : '— session ended —'}</span>
        </div>
      )}
      <div
        className="terminal-surface"
        ref={element}
        aria-label={hostNode ? 'Interactive node terminal' : 'Interactive pod terminal'}
      />
      <div className="log-footer">
        <span>
          {connected
            ? hostNode
              ? 'Root shell on the selected Linux node'
              : 'Commands run inside the selected container'
            : 'Terminal is not connected'}
        </span>
        <span>500-line scrollback · 10-minute maximum · 2-minute idle timeout</span>
      </div>
      <Note>
        Leaving this terminal page closes its session.{' '}
        {hostNode
          ? 'Host authority is checked independently from project and administrator roles.'
          : 'A database connection requires its normal database permissions.'}
      </Note>
    </section>
  )
}
