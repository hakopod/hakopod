import { useEffect, useRef, useState } from 'react'
import type { components } from '../lib/api.generated'
import type { Application } from '../lib/types'
import { client, unwrap } from '../lib/client'
import { message, timestamp } from '../lib/api'
import { Button } from './ui/button'
import { RequestError } from './shared'
import { useScope, canAccess } from '../lib/scope'

type Result = components['schemas']['BindingTestResult']

const outcomes: Record<Result['outcome'], string> = {
  passed: 'Connection verified',
  failed: 'Connection failed',
  unsupported: 'Protocol not supported by this test',
  unavailable: 'Test unavailable',
  stale: 'Current configuration not verified',
}
const stages: Record<string, string> = {
  runtime: 'Application runtime',
  configuration: 'Loaded variable',
  capability: 'Protocol support',
  dns: 'DNS',
  network: 'Network',
  certificate: 'TLS certificate',
  authentication: 'Authentication',
  query: 'Read-only query',
}

export function BindingConnectionTest({
  application,
  service,
  variable,
}: {
  application: Application
  service: string
  variable: string
}) {
  const { identity } = useScope()
  const canTest = canAccess(identity, application.project, 'deployments:write')
  const [busy, setBusy] = useState(false)
  const [inspection, setInspection] = useState<components['schemas']['BindingInspection']>()
  const [error, setError] = useState('')
  const [result, setResult] = useState<Result>()
  const active = useRef<AbortController | null>(null)
  useEffect(
    () => () => {
      const request = active.current
      active.current = null
      request?.abort()
    },
    [],
  )

  const inspect = async () => {
    if (active.current) return
    const controller = new AbortController()
    active.current = controller
    const timeout = setTimeout(() => controller.abort(), 12000)
    setBusy(true)
    setError('')
    try {
      const value = await unwrap(
        client.GET('/applications/{id}/services/{service}/bindings/{variable}', {
          signal: controller.signal,
          params: { path: { id: application.id, service, variable } },
        }),
      )
      if (!controller.signal.aborted) {
        setInspection(value)
        setResult(value.last_test)
      }
    } catch (cause) {
      if (active.current === controller) setError(message(cause))
    } finally {
      clearTimeout(timeout)
      if (active.current === controller) {
        active.current = null
        setBusy(false)
      }
    }
  }

  const test = async () => {
    if (active.current) return
    const controller = new AbortController()
    active.current = controller
    const timeout = setTimeout(() => controller.abort(), 25000)
    setBusy(true)
    setError('')
    setResult(undefined)
    setInspection(undefined)
    try {
      const value = await unwrap(
        client.POST('/applications/{id}/services/{service}/bindings/{variable}/test', {
          signal: controller.signal,
          params: { path: { id: application.id, service, variable } },
          body: { expected_revision: application.revision },
        }),
      )
      if (!controller.signal.aborted) setResult(value)
    } catch (cause) {
      if (controller.signal.aborted) {
        if (active.current === controller)
          setError('The connection test did not finish. Refresh the service and try again.')
      } else {
        setError(message(cause))
      }
    } finally {
      clearTimeout(timeout)
      if (active.current === controller) {
        active.current = null
        setBusy(false)
      }
    }
  }

  return (
    <div className="flex min-w-0 basis-full flex-wrap items-center gap-2">
      <Button
        size="sm"
        className="min-h-10"
        disabled={busy}
        onClick={() => void inspect()}
        aria-label={`Inspect ${variable} binding in ${service}`}
      >
        {inspection ? 'Refresh binding' : 'Inspect binding'}
      </Button>
      <Button
        size="sm"
        className="min-h-10"
        disabled={busy || !canTest}
        onClick={() => void test()}
        aria-label={`Test ${variable} connection from ${service}`}
      >
        {busy ? 'Testing connection…' : 'Test connection'}
      </Button>
      {inspection && (
        <div className="min-w-0 basis-full" role="status" aria-live="polite">
          <ol
            className="grid gap-3 py-2 sm:grid-cols-2 xl:grid-cols-4"
            aria-label="Binding verification stages"
          >
            {inspection.steps.map((step) => (
              <li key={step.name} className="min-w-0">
                <strong className="text-xs">
                  {
                    {
                      saved: 'Saved',
                      resolved: 'Resolved',
                      loaded: 'Loaded by container',
                      connection:
                        step.status === 'passed' ? 'Connection verified' : 'Connection test',
                    }[step.name]
                  }{' '}
                  ·{' '}
                  {
                    {
                      passed: 'Verified',
                      failed: 'Failed',
                      unknown: 'Not checked',
                      stale: 'Stale',
                      unsupported: 'Unsupported',
                    }[step.status]
                  }
                </strong>
                <p className="mt-1 text-xs text-muted-foreground">{step.message}</p>
              </li>
            ))}
          </ol>
          <p className="text-xs text-muted-foreground">
            Checked {timestamp(inspection.observed_at)}. Connection evidence expires after five
            minutes and covers one container.
          </p>
        </div>
      )}
      {error && (
        <div className="min-w-0 basis-full">
          <RequestError error={error} />
        </div>
      )}
      {result && (
        <div className="min-w-0 basis-full" role="status" aria-live="polite">
          <div className="flex flex-wrap items-baseline gap-x-3 gap-y-1 text-sm">
            <strong>
              {inspection
                ? `${inspection.steps.some(
                    (step) => step.name === 'connection' && step.status === 'unsupported',
                  ) ? 'Previous' : 'Last'} test: ${outcomes[result.outcome].toLowerCase()}`
                : outcomes[result.outcome]}
            </strong>
            <span className="text-xs text-muted-foreground">{timestamp(result.observed_at)}</span>
          </div>
          {result.pod && (
            <p className="mt-1 break-all text-xs text-muted-foreground">
              Tested pod <code>{result.pod}</code> · saved revision {result.revision}
            </p>
          )}
          {result.loaded_matches_snapshot === false && (
            <p className="mt-2 text-sm">
              This pod uses older connection settings. Wait for the rollout, then test again.
            </p>
          )}
          {result.outcome === 'stale' && result.loaded_matches_snapshot === null && (
            <p className="mt-2 text-sm">
              The connection worked, but its loaded values could not be matched to the current
              settings. Refresh and test again.
            </p>
          )}
          {result.stages.some((stage) => stage.code === 'tls_not_configured') && (
            <p className="mt-2 text-sm">
              This binding is configured without TLS. The connection is unencrypted.
            </p>
          )}
          <details className="mt-2 text-sm" open={result.outcome !== 'passed'}>
            <summary className="flex min-h-11 cursor-pointer items-center rounded-sm py-1 focus-visible:outline-2 focus-visible:outline-offset-2">
              Connection checks
            </summary>
            <ul className="mt-2 grid gap-2">
              {result.stages.map((stage, index) => (
                <li
                  key={`${index}-${stage.code}`}
                  className="grid min-w-0 gap-1 sm:grid-cols-[10rem_minmax(0,1fr)] sm:gap-3"
                >
                  <strong className="text-xs">
                    {stages[stage.name] || stage.name} ·{' '}
                    {stage.status === 'passed'
                      ? 'Passed'
                      : stage.status === 'unsupported'
                        ? 'Unsupported'
                        : stage.status === 'skipped'
                          ? 'Skipped'
                          : 'Failed'}
                  </strong>
                  <span className="min-w-0 text-xs text-muted-foreground">{stage.message}</span>
                </li>
              ))}
            </ul>
          </details>
        </div>
      )}
    </div>
  )
}
