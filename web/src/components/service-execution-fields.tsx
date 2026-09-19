import { useId } from 'react'
import { useQuery } from '@tanstack/react-query'
import { client, unwrap } from '../lib/client'
import type { Service } from '../lib/types'
import { fieldError } from '../lib/form-errors'
import { httpFunction, serverlessDefaults } from '../lib/http-functions'
import { Input } from './ui/input'
import { Textarea } from './ui/textarea'
import { SelectField } from './ui/select'
import { Button } from './ui/button'

export function FunctionEditor({
  service,
  disabled,
  onChange,
}: {
  service: Service
  disabled?: boolean
  onChange: (value: Partial<Service>) => void
}) {
  const file = service.files?.function
  if (
    !file ||
    file.content === undefined ||
    !['/app/function.mjs', '/app/function.py'].includes(file.mount_path)
  )
    return null
  return (
    <label className="grid min-w-0 gap-2">
      {file.mount_path.endsWith('.py') ? 'Python HTTP function' : 'JavaScript HTTP function'}
      <Textarea
        className="min-h-72! font-mono text-sm"
        spellCheck={false}
        value={file.content}
        disabled={disabled}
        maxLength={32768}
        onChange={(e) =>
          onChange({ files: { ...service.files, function: { ...file, content: e.target.value } } })
        }
      />
      <span className="field-help">
        Listen on 0.0.0.0:{service.port}. Source is saved with the deployment configuration. Use
        secret variables for credentials. For additional packages, build a container from your Git
        repository.
      </span>
    </label>
  )
}

export function ServiceExecutionFields({
  name,
  service,
  project,
  environment,
  application,
  disabled,
  error,
  onChange,
  onStarter,
}: {
  name: string
  service: Service
  project: string
  environment: string
  application: string
  disabled?: boolean
  error: string
  onChange: (value: Partial<Service>) => void
  onStarter: (value: Service) => void
}) {
  const id = useId()
  const nodes = useQuery({
    queryKey: ['placement-nodes', project, environment, application],
    queryFn: ({ signal }) =>
      unwrap(
        client.GET('/placement/nodes', {
          signal,
          params: { query: { project, environment, application } },
        }),
      ),
    enabled: Boolean(project && environment),
    staleTime: 30_000,
  })
  const available = !nodes.error && nodes.data?.serverless_available === true
  const incompatible = Boolean(
    service.job ||
    service.autoscaling ||
    service.volume ||
    service.mounts?.length ||
    service.gpu ||
    service.public_tcp?.length ||
    service.ports?.length ||
    Object.keys(service.http || {}).length ||
    service.certificate_mounts?.length,
  )
  const selectedMissing =
    service.node_name && !nodes.data?.items.some((n) => n.name === service.node_name)
  return (
    <div className="grid min-w-0 gap-4">
      <div className="grid gap-1">
        <span id={`${id}-placement`}>Run on node</span>
        <SelectField
          label={`${name} node placement`}
          value={service.node_name || ''}
          disabled={disabled || nodes.isPending || Boolean(nodes.error)}
          error={fieldError(error, `services.${name}.node_name`)}
          onValueChange={(value) => onChange({ node_name: value || undefined })}
          options={[
            { value: '', label: 'Automatic · any eligible node' },
            ...(selectedMissing
              ? [
                  {
                    value: service.node_name!,
                    label: `${service.node_name} · ${nodes.data ? 'unavailable' : 'saved selection'}`,
                    disabled: true,
                  },
                ]
              : []),
            ...(nodes.data?.items || []).map((n) => ({
              value: n.name,
              label: `${n.name} · ${n.architecture}${n.available ? '' : ` · ${n.reason}`}`,
              disabled: !n.available,
            })),
          ]}
        />
        <p className="field-help">
          Pin to a node for local data or dedicated compute. If that node is unavailable, the
          service waits there instead of moving elsewhere.
        </p>
        {nodes.isPending && (
          <p className="field-help" role="status">
            Loading available nodes…
          </p>
        )}
        {nodes.error && (
          <div className="flex flex-wrap items-center gap-2 text-sm">
            <span className="field-error">
              Node options could not be loaded. Your selection is preserved.
            </span>
            <Button size="sm" disabled={nodes.isFetching} onClick={() => void nodes.refetch()}>
              Retry
            </Button>
          </div>
        )}
      </div>
      <div className="grid gap-2">
        <label className="flex min-h-11 items-center gap-2">
          <input
            type="checkbox"
            checked={Boolean(service.serverless)}
            disabled={disabled || (!service.serverless && (!available || incompatible))}
            onChange={(e) =>
              onChange(
                e.target.checked
                  ? {
                      serverless: { ...serverlessDefaults },
                      public: true,
                      port: service.port || 8080,
                      replicas: 1,
                    }
                  : { serverless: undefined },
              )
            }
          />
          Serverless HTTP
        </label>
        <p className="field-help">
          Serve public HTTP requests and sleep when idle. The first request waits for the container
          to start. Keep background workers and persistent data in regular services.
        </p>
        {!available && !nodes.isPending && (
          <p className="field-help">
            {nodes.error
              ? 'Serverless availability could not be checked.'
              : 'The installation owner must enable the activation gateway before using serverless.'}
          </p>
        )}
        {incompatible && (
          <p className="field-help">
            Remove jobs, autoscaling, persistent mounts, GPU, extra ports and backend certificates
            to enable serverless.
          </p>
        )}
        {service.serverless && (
          <>
            <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
              <div className="grid gap-1">
                <span>Minimum replicas</span>
                <SelectField
                  label={`${name} minimum replicas`}
                  value={String(service.serverless.min_replicas ?? 0)}
                  disabled={disabled}
                  options={[
                    { value: '0', label: '0 · sleep when idle' },
                    { value: '1', label: '1 · always keep warm' },
                  ]}
                  onValueChange={(v) =>
                    onChange({
                      serverless: {
                        ...serverlessDefaults,
                        ...service.serverless,
                        min_replicas: Number(v),
                      },
                    })
                  }
                />
              </div>
              {(
                [
                  ['idle_seconds', 'Sleep after idle (seconds)', 30, 86400, 300],
                  ['startup_timeout_seconds', 'Cold-start wait (seconds)', 5, 300, 60],
                  ['request_timeout_seconds', 'Request time limit (seconds)', 1, 300, 60],
                  ['max_concurrency', 'Concurrent requests', 1, 64, 16],
                ] as const
              ).map(([key, label, min, max, fallback]) => (
                <label key={key}>
                  {label}
                  <Input
                    type="number"
                    min={min}
                    max={max}
                    required
                    disabled={
                      disabled || (key === 'idle_seconds' && service.serverless?.min_replicas === 1)
                    }
                    value={service.serverless?.[key] ?? fallback}
                    aria-label={`${name} ${label}`}
                    error={fieldError(error, `services.${name}.serverless`)}
                    onChange={(e) =>
                      onChange({
                        serverless: {
                          ...serverlessDefaults,
                          ...service.serverless,
                          [key]: Number(e.target.value),
                        },
                      })
                    }
                  />
                </label>
              ))}
            </div>
            <p className="field-help">
              {service.serverless.min_replicas === 1
                ? 'Keeps one container running.'
                : 'Scales between zero and one container.'}{' '}
              Excess concurrent requests receive HTTP 429; bodies are limited to 4 MiB. Use the
              public URL for wake-up and activity tracking.
            </p>
          </>
        )}
      </div>
      {!service.image && (
        <div className="flex flex-wrap gap-2">
          <Button
            disabled={disabled || !available || incompatible}
            onClick={() => onStarter(httpFunction('javascript'))}
          >
            Start a JavaScript function
          </Button>
          <Button
            disabled={disabled || !available || incompatible}
            onClick={() => onStarter(httpFunction('python'))}
          >
            Start a Python function
          </Button>
        </div>
      )}
    </div>
  )
}
