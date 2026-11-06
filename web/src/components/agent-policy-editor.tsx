import { useEffect, useMemo, useState, type ReactNode } from 'react'
import { Link, useNavigate } from '@tanstack/react-router'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { Switch } from '@hakopod/hatch-ui/components/switch'
import { Button } from './ui/button'
import { Dialog } from './ui/dialog'
import { Field } from './ui/field'
import { SelectField } from './ui/select'
import { Icon } from './icons'
import { HeadingHelp } from './shared'
import { ChoiceCards } from './create-flow'
import { ConditionRows, ValuePicker, type Suggestion } from './agent-policy-conditions'
import { AgentPolicyImpact, DecisionChip } from './agent-policy-preview'
import { client, unwrap } from '../lib/client'
import { APIError, message, relative } from '../lib/api'
import { useProjects } from '../lib/projects'
import {
  agentPoliciesKey,
  agentPolicyKey,
  agentPolicyVersionsKey,
  createAgentPolicy,
  deleteAgentPolicy,
  emptyDocument,
  publishAgentPolicy,
  saveAgentPolicyDraft,
  simulateAgentPolicy,
  statusLabels,
  useAgentPolicy,
  type AgentPolicy,
  type AgentPolicyApprover,
  type AgentPolicyDocument,
  type AgentPolicySimulation,
} from '../lib/agent-policies'
import {
  approvalTTLOptions,
  categoryLabels,
  credentialTTLOptions,
  policyCategories,
  policyTemplates,
  summarizePolicy,
  validatePolicy,
} from '../lib/agent-policy-form'
import { operationOptions } from '../lib/agent-policy-operations'

function failure(error: unknown) {
  if (error instanceof APIError && error.status === 409)
    return 'Someone changed this policy after you opened it. Reload to get the latest version; your edits stay here until you leave.'
  return message(error)
}

const approverValue = (a: AgentPolicyApprover) =>
  a.kind === 'project_admins' ? 'project_admins' : `${a.kind}:${a.id ?? ''}`
function approverFrom(value: string): AgentPolicyApprover | null {
  if (value === 'project_admins') return { kind: 'project_admins' }
  const at = value.indexOf(':')
  const kind = value.slice(0, at)
  const id = value.slice(at + 1)
  return (kind === 'team' || kind === 'identity') && id ? { kind, id } : null
}

function useLookups() {
  const projects = useProjects()
  const teams = useQuery({
    queryKey: ['teams'],
    queryFn: ({ signal }) => unwrap(client.GET('/teams', { signal })),
    staleTime: 30000,
  })
  const keys = useQuery({
    queryKey: ['keys'],
    queryFn: ({ signal }) => unwrap(client.GET('/keys', { signal })),
    staleTime: 30000,
  })
  const users = useQuery({
    queryKey: ['users'],
    queryFn: ({ signal }) => unwrap(client.GET('/users', { signal })),
    staleTime: 30000,
    retry: false,
  })
  return useMemo(() => {
    const projectList = projects.data?.items ?? []
    const teamList = (teams.data?.items ?? []).map((t) => ({ value: t.id, label: t.name }))
    const keyList = (keys.data?.items ?? []).map((k) => ({ value: k.id, label: k.name }))
    const userList = (users.data?.items ?? []).map((u) => ({
      value: u.id,
      label: u.name || u.email,
      detail: u.name ? u.email : undefined,
    }))
    const environments = [...new Set(projectList.flatMap((p) => p.environments.map((e) => e.name)))]
    const suggestions: Record<AgentPolicyDocument['when'][number]['field'], Suggestion[]> = {
      team: teamList,
      key: keyList,
      category: policyCategories.map((c) => ({ value: c, label: categoryLabels[c] })),
      operation: operationOptions,
      project: projectList.map((p) => ({ value: p.id, label: p.name })),
      environment: environments.map((e) => ({ value: e, label: e })),
    }
    const approvers: Suggestion[] = [
      {
        value: 'project_admins',
        label: 'Project administrators',
        detail: 'of the project the request acts on',
      },
      ...teamList.map((t) => ({ value: `team:${t.value}`, label: t.label, detail: 'Team' })),
      ...userList.map((u) => ({ value: `identity:${u.value}`, label: u.label, detail: u.detail })),
    ]
    const names = new Map(
      [...teamList, ...keyList, ...userList, ...suggestions.project].map((s) => [s.value, s.label]),
    )
    return { suggestions, approvers, name: (id: string) => names.get(id) ?? id }
  }, [projects.data, teams.data, keys.data, users.data])
}

export function AgentPolicyEditor({ policyId }: { policyId?: string }) {
  const query = useAgentPolicy(policyId ?? '')
  const [template, setTemplate] = useState<(typeof policyTemplates)[number] | 'blank' | null>(null)
  if (!policyId) {
    if (!template) return <TemplatePicker onPick={setTemplate} />
    return template === 'blank' ? (
      <Editor />
    ) : (
      <Editor key={template.id} seed={{ name: template.name, document: template.document }} />
    )
  }
  if (query.isPending)
    return <p className="py-8 text-sm text-[var(--muted-foreground)]">Loading policy…</p>
  if (query.isError)
    return (
      <p role="alert" className="py-8 text-sm text-[var(--error-text)]">
        {message(query.error)}
      </p>
    )
  return <Editor key={query.data.id} policy={query.data} />
}

function BackLink() {
  return (
    <Link
      to="/settings"
      search={{ tab: 'agent-policies' }}
      className="inline-flex items-center gap-1 text-sm text-[var(--muted-foreground)] hover:text-[var(--foreground)] focus-visible:outline-2"
    >
      <Icon name="back" size={14} />
      Agent policies
    </Link>
  )
}

function TemplatePicker({
  onPick,
}: {
  onPick: (template: (typeof policyTemplates)[number] | 'blank') => void
}) {
  return (
    <div className="grid min-w-0 gap-5 py-2">
      <div className="grid gap-1">
        <BackLink />
        <div className="flex items-center gap-1.5">
          <h1 className="text-[length:var(--type-page-size)] leading-[var(--type-page-line)] font-medium">
            New agent policy
          </h1>
          <HeadingHelp title="New agent policy">
            Start from a common rule and adjust it, or start empty. Nothing applies until you
            publish.
          </HeadingHelp>
        </div>
      </div>
      <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
        {policyTemplates.map((template) => (
          <button
            key={template.id}
            type="button"
            className="group grid content-start gap-2 rounded-[var(--radius-card)] border border-[var(--hairline)] bg-[var(--card)] p-4 text-left transition-colors hover:border-[var(--muted)] focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-[var(--navigation-active)]"
            onClick={() => onPick(template)}
          >
            <span className="flex items-center justify-between gap-2">
              <strong className="text-[15px] font-medium">{template.title}</strong>
              <span
                className={`rounded-full px-2 py-0.5 text-[11px] ${template.document.then.effect === 'block' ? 'bg-red-500/10 text-red-700 dark:text-red-300' : 'bg-amber-500/10 text-amber-700 dark:text-amber-300'}`}
              >
                {template.document.then.effect === 'block' ? 'Blocks' : 'Needs approval'}
              </span>
            </span>
            <span className="text-sm text-[var(--muted-foreground)]">{template.detail}</span>
          </button>
        ))}
        <button
          type="button"
          className="grid content-start gap-2 rounded-[var(--radius-card)] border border-dashed border-[var(--hairline)] p-4 text-left transition-colors hover:border-[var(--muted)] focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-[var(--navigation-active)]"
          onClick={() => onPick('blank')}
        >
          <strong className="text-[15px] font-medium">Start empty</strong>
          <span className="text-sm text-[var(--muted-foreground)]">
            Write your own conditions from scratch.
          </span>
        </button>
      </div>
    </div>
  )
}

function Section({
  title,
  help,
  children,
}: {
  title: string
  help?: ReactNode
  children: ReactNode
}) {
  return (
    <section className="min-w-0 rounded-[var(--radius-card)] border border-[var(--hairline)] bg-[var(--card)]">
      <header className="flex items-center gap-1.5 border-b border-[var(--hairline)] px-4 py-3">
        <h2 className="text-[15px] leading-6 font-medium">{title}</h2>
        {help && <HeadingHelp title={title}>{help}</HeadingHelp>}
      </header>
      <div className="@container grid gap-4 p-4">{children}</div>
    </section>
  )
}

// A labelled row inside a section: label and hint on the left, control on the right.
function Row({ label, hint, children }: { label: string; hint?: string; children: ReactNode }) {
  return (
    <div className="grid min-w-0 gap-2 @lg:grid-cols-[11rem_minmax(0,1fr)] @lg:items-start @lg:gap-4">
      <div className="grid gap-0.5 @lg:pt-2">
        <span className="text-sm font-medium">{label}</span>
        {hint && <span className="text-xs text-[var(--muted-foreground)]">{hint}</span>}
      </div>
      <div className="min-w-0">{children}</div>
    </div>
  )
}

const statusTone: Record<AgentPolicy['status'], string> = {
  enforced: 'bg-emerald-500/10 text-emerald-700 dark:text-emerald-300',
  monitor: 'bg-sky-500/10 text-sky-700 dark:text-sky-300',
  draft: 'bg-[var(--surface-2)] text-[var(--muted-foreground)]',
}

function Editor({
  policy,
  seed,
}: {
  policy?: AgentPolicy
  seed?: { name: string; document: AgentPolicyDocument }
}) {
  const queryClient = useQueryClient()
  const navigate = useNavigate()
  const lookups = useLookups()
  const initial = policy?.draft ?? policy?.document ?? seed?.document ?? emptyDocument()
  const initialName = policy?.name ?? seed?.name ?? ''
  const [current, setCurrent] = useState<AgentPolicy | undefined>(policy)
  const [name, setName] = useState(initialName)
  const [doc, setDoc] = useState<AgentPolicyDocument>(initial)
  const [baseline, setBaseline] = useState(
    JSON.stringify(policy ? [initialName, initial] : ['', emptyDocument()]),
  )
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  const [review, setReview] = useState(false)
  const [remove, setRemove] = useState(false)
  const [publishAs, setPublishAs] = useState<'enforced' | 'monitor'>('monitor')

  const errors = validatePolicy(name, doc)
  const valid = Object.keys(errors).length === 0
  const dirty = JSON.stringify([name, doc]) !== baseline
  const then = doc.then
  const setThen = (patch: Partial<AgentPolicyDocument['then']>) =>
    setDoc((d) => ({ ...d, then: { ...d.then, ...patch } }))
  const summary = summarizePolicy(doc, lookups.name)

  const remember = (saved: AgentPolicy) => {
    setCurrent(saved)
    queryClient.setQueryData(agentPolicyKey(saved.id), saved)
    void queryClient.invalidateQueries({ queryKey: agentPoliciesKey, exact: true })
  }
  const persist = async () => {
    const saved = current
      ? await saveAgentPolicyDraft(current, name.trim(), doc)
      : await createAgentPolicy(name.trim(), doc)
    remember(saved)
    setBaseline(JSON.stringify([name, doc]))
    return saved
  }
  const open = (id: string) =>
    void navigate({ to: '/settings/agent-policies/$policyId', params: { policyId: id } })
  const run = async (action: () => Promise<void>) => {
    setBusy(true)
    setError('')
    try {
      await action()
    } catch (reason) {
      setError(failure(reason))
    } finally {
      setBusy(false)
    }
  }
  const save = () =>
    run(async () => {
      const saved = await persist()
      if (!current) open(saved.id)
    })
  const publish = () =>
    run(async () => {
      const saved = dirty || !current ? await persist() : current
      const published = await publishAgentPolicy(saved, publishAs)
      remember(published)
      void queryClient.invalidateQueries({ queryKey: agentPolicyVersionsKey(published.id) })
      setReview(false)
      if (!current) open(published.id)
    })
  const destroy = () =>
    run(async () => {
      if (!current) return
      await deleteAgentPolicy(current)
      queryClient.removeQueries({ queryKey: agentPolicyKey(current.id) })
      void queryClient.invalidateQueries({ queryKey: agentPoliciesKey, exact: true })
      void navigate({ to: '/settings', search: { tab: 'agent-policies' } })
    })

  const nextVersion = (current?.current_version ?? 0) + 1
  const fieldError = (key: string) =>
    errors[key] ? <p className="mt-1 text-xs text-[var(--error-text)]">{errors[key]}</p> : null

  return (
    <div className="grid min-w-0 gap-5 pt-2">
      <header className="grid gap-1.5">
        <BackLink />
        <div className="flex min-w-0 flex-wrap items-center gap-x-3 gap-y-1.5">
          <h1 className="min-w-0 text-[length:var(--type-page-size)] leading-[var(--type-page-line)] font-medium [overflow-wrap:anywhere]">
            {name.trim() || 'New agent policy'}
          </h1>
          {current && (
            <span
              className={`rounded-full px-2.5 py-0.5 text-xs font-medium ${statusTone[current.status]}`}
            >
              {statusLabels[current.status]}
            </span>
          )}
          {current && (
            <span className="text-xs text-[var(--muted-foreground)]">
              {current.current_version > 0 && `v${current.current_version} · `}Edited by{' '}
              {current.updated_by_name} {relative(current.updated_at)}
            </span>
          )}
          {current && (
            <Button
              type="button"
              variant="ghost"
              size="sm"
              className="ml-auto"
              onClick={() => setRemove(true)}
            >
              <Icon name="trash" size={14} />
              Delete
            </Button>
          )}
        </div>
      </header>

      <div className="grid min-w-0 gap-5 lg:grid-cols-[minmax(0,1fr)_22rem]">
        <form
          className="grid min-w-0 content-start gap-4"
          onSubmit={(event) => {
            event.preventDefault()
            if (valid) void save()
          }}
        >
          <div
            className="flex items-start gap-3 rounded-[var(--radius-card)] border border-[var(--hairline)] bg-[var(--surface-2)] px-4 py-3"
            aria-live="polite"
          >
            <span
              className={`mt-1 size-2 shrink-0 rounded-full ${then.effect === 'block' ? 'bg-red-500' : 'bg-amber-500'}`}
              aria-hidden
            />
            <p className="text-sm leading-6">{summary}</p>
          </div>

          <Section title="Details">
            <div className="grid gap-4 sm:grid-cols-2">
              <div>
                <Field
                  label="Name"
                  value={name}
                  maxLength={100}
                  placeholder="Production deploys need approval"
                  onChange={(event) => setName(event.target.value)}
                  aria-invalid={Boolean(errors.name) || undefined}
                />
                {fieldError('name')}
              </div>
              <div>
                <Field
                  label="Description"
                  value={doc.description}
                  maxLength={200}
                  placeholder="Shown in the policy list"
                  onChange={(event) => setDoc({ ...doc, description: event.target.value })}
                />
                {fieldError('description')}
              </div>
            </div>
          </Section>

          <Section
            title="Applies to"
            help="A request made with an API key matches when every condition is true. Policies never give a key access it does not already have."
          >
            <ConditionRows
              conditions={doc.when}
              onChange={(when) => setDoc({ ...doc, when })}
              suggestions={lookups.suggestions}
              errors={errors}
            />
          </Section>

          <Section title="Decision">
            <ChoiceCards
              label="What happens to a matching request"
              compact
              value={then.effect}
              onChange={(effect) =>
                setThen(
                  effect === 'block'
                    ? { effect, approvers: undefined, approval_ttl_seconds: undefined }
                    : {
                        effect: 'require_approval',
                        approvers: [{ kind: 'project_admins' }],
                        approval_ttl_seconds: 900,
                      },
                )
              }
              choices={[
                {
                  value: 'require_approval',
                  title: 'Require approval',
                  description: 'The request waits until a person approves it, then runs once.',
                },
                {
                  value: 'block',
                  title: 'Block',
                  description: 'The request is refused. Nobody can approve it.',
                },
              ]}
            />
            {then.effect === 'require_approval' && (
              <>
                <Row label="Approvers" hint="Leave empty for installation administrators">
                  <ValuePicker
                    label="Approvers"
                    values={(then.approvers ?? []).map(approverValue)}
                    onChange={(values) =>
                      setThen({
                        approvers: values
                          .map(approverFrom)
                          .filter((a): a is AgentPolicyApprover => a !== null),
                      })
                    }
                    options={lookups.approvers}
                    freeText={false}
                    error={Boolean(errors.approvers)}
                    placeholder="Choose teams or people"
                  />
                  {fieldError('approvers')}
                </Row>
                <Row label="Time to decide" hint="After this the request expires">
                  <div className="max-w-56">
                    <SelectField
                      label="Time to decide"
                      value={String(then.approval_ttl_seconds ?? '')}
                      onValueChange={(value) => setThen({ approval_ttl_seconds: Number(value) })}
                      options={approvalTTLOptions}
                      error={errors.approval_ttl}
                    />
                  </div>
                  {fieldError('approval_ttl')}
                </Row>
              </>
            )}
            <Row label="Key lifetime" hint="For keys created by a matching request">
              <div className="max-w-56">
                <SelectField
                  label="Key lifetime"
                  value={String(then.max_credential_ttl_seconds ?? '')}
                  onValueChange={(value) =>
                    setThen({ max_credential_ttl_seconds: value ? Number(value) : undefined })
                  }
                  options={credentialTTLOptions}
                />
              </div>
              {fieldError('credential_ttl')}
            </Row>
          </Section>

          <Section title="Exception">
            <div className="flex items-start justify-between gap-4">
              <label htmlFor="policy-except" className="grid gap-0.5">
                <span className="text-sm font-medium">Let requests through during an alarm</span>
                <span className="text-xs text-[var(--muted-foreground)]">
                  While an alarm is active in the request's project and environment, matching
                  requests run, are recorded, and send a Slack notice.
                </span>
              </label>
              <Switch
                id="policy-except"
                checked={Boolean(doc.except?.active_incident)}
                onCheckedChange={(active_incident) =>
                  setDoc({ ...doc, except: active_incident ? { active_incident } : undefined })
                }
              />
            </div>
          </Section>

          {error && (
            <p role="alert" className="text-sm text-[var(--error-text)]">
              {error}
            </p>
          )}
        </form>

        <aside className="grid min-w-0 content-start gap-4 lg:sticky lg:top-4 lg:self-start">
          <div className="rounded-[var(--radius-card)] border border-[var(--hairline)] bg-[var(--card)] p-4">
            <AgentPolicyImpact document={doc} valid={valid} />
          </div>
          <div className="rounded-[var(--radius-card)] border border-[var(--hairline)] bg-[var(--card)] p-4">
            <TestRequest
              document={doc}
              valid={valid}
              policyId={current?.id}
              lookups={lookups.suggestions}
            />
          </div>
        </aside>
      </div>

      <footer className="sticky bottom-0 z-10 -mx-4 flex items-center gap-2 border-t border-[var(--hairline)] bg-[var(--background)] px-4 py-3 sm:-mx-6 sm:px-6">
        <span
          className="mr-auto hidden text-xs text-[var(--muted-foreground)] sm:inline"
          aria-live="polite"
        >
          {!valid
            ? 'Complete the highlighted fields to save.'
            : dirty
              ? 'Unsaved changes'
              : current?.draft
                ? 'Draft saved. Publish to apply it.'
                : current
                  ? 'All changes published'
                  : ''}
        </span>
        <Button
          type="button"
          className="flex-1 sm:flex-none"
          disabled={!valid || busy || (!dirty && !!current)}
          onClick={save}
        >
          Save draft
        </Button>
        <Button
          type="button"
          variant="primary"
          className="flex-1 sm:flex-none"
          disabled={!valid || busy}
          onClick={() => {
            setPublishAs(current && current.status !== 'draft' ? current.status : 'monitor')
            setReview(true)
          }}
        >
          Review and publish
        </Button>
      </footer>

      <Dialog
        open={review}
        onOpenChange={setReview}
        title={`Publish version ${nextVersion}`}
        description="Published versions apply to API key requests within a few seconds."
      >
        <div className="grid gap-4">
          <p className="rounded-[var(--radius-card)] bg-[var(--surface-2)] px-3 py-2.5 text-sm leading-6">
            {summary}
          </p>
          <ChoiceCards
            label="Publish as"
            compact
            value={publishAs}
            onChange={setPublishAs}
            choices={[
              {
                value: 'monitor',
                title: 'Monitor',
                description: 'Record what would happen. Nothing is blocked.',
              },
              {
                value: 'enforced',
                title: 'Enforce',
                description: 'Block or hold matching requests now.',
              },
            ]}
          />
          {publishAs === 'enforced' && current?.status !== 'enforced' && (
            <p className="text-xs text-[var(--muted-foreground)]">
              Tip: publish as Monitor first and check the impact preview before enforcing.
            </p>
          )}
          {dirty && (
            <p className="text-xs text-[var(--muted-foreground)]">
              Your unsaved changes are saved as part of this version.
            </p>
          )}
          {error && (
            <p role="alert" className="text-sm text-[var(--error-text)]">
              {error}
            </p>
          )}
          <div className="flex justify-end gap-2">
            <Button type="button" onClick={() => setReview(false)}>
              Cancel
            </Button>
            <Button type="button" variant="primary" disabled={busy} onClick={publish}>
              {publishAs === 'enforced' ? 'Publish and enforce' : 'Publish to monitor'}
            </Button>
          </div>
        </div>
      </Dialog>

      <Dialog
        open={remove}
        onOpenChange={setRemove}
        title="Delete this policy?"
        description="It stops applying immediately and its versions are removed. The audit log records the deletion."
      >
        {error && (
          <p role="alert" className="text-sm text-[var(--error-text)]">
            {error}
          </p>
        )}
        <div className="flex justify-end gap-2">
          <Button type="button" onClick={() => setRemove(false)}>
            Cancel
          </Button>
          <Button type="button" variant="danger" disabled={busy} onClick={destroy}>
            <Icon name="trash" size={14} />
            Delete policy
          </Button>
        </div>
      </Dialog>
    </div>
  )
}

function TestRequest({
  document,
  valid,
  policyId,
  lookups,
}: {
  document: AgentPolicyDocument
  valid: boolean
  policyId?: string
  lookups: Record<AgentPolicyDocument['when'][number]['field'], Suggestion[]>
}) {
  const [keyId, setKeyId] = useState('')
  const [operation, setOperation] = useState<string[]>([])
  const [environment, setEnvironment] = useState<string[]>([])
  const [alarm, setAlarm] = useState(false)
  const [result, setResult] = useState<AgentPolicySimulation | null>(null)
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  const documentKey = JSON.stringify(document)
  useEffect(() => setResult(null), [keyId, operation, environment, alarm, documentKey])
  const run = async () => {
    setBusy(true)
    setError('')
    try {
      setResult(
        await simulateAgentPolicy({
          key_id: keyId,
          operation: operation[0],
          environment: environment[0] || undefined,
          active_incident: alarm,
          policy_id: policyId,
          document,
        }),
      )
    } catch (reason) {
      setError(message(reason))
    } finally {
      setBusy(false)
    }
  }
  return (
    <form
      className="grid gap-3"
      onSubmit={(event) => {
        event.preventDefault()
        void run()
      }}
    >
      <div className="flex items-center gap-1.5">
        <h2 className="text-sm font-medium">Test a request</h2>
        <HeadingHelp title="Test a request">
          Check how one request would be decided by your edits together with every other published
          policy.
        </HeadingHelp>
      </div>
      <SelectField
        label="API key"
        required
        value={keyId}
        onValueChange={setKeyId}
        options={[{ value: '', label: 'Choose a key' }, ...lookups.key]}
      />
      <div className="grid gap-1">
        <span className="text-[13px] font-medium">Operation</span>
        <ValuePicker
          label="Operation"
          values={operation}
          onChange={(values) => setOperation(values.slice(-1))}
          options={lookups.operation}
          freeText={false}
          placeholder="Search operations"
        />
      </div>
      <div className="grid gap-1">
        <span className="text-[13px] font-medium">Environment</span>
        <ValuePicker
          label="Environment"
          values={environment}
          onChange={(values) => setEnvironment(values.slice(-1))}
          options={lookups.environment}
          placeholder="Any environment"
        />
      </div>
      <label className="flex items-center justify-between gap-2 text-sm">
        An alarm is active
        <Switch checked={alarm} onCheckedChange={setAlarm} />
      </label>
      <Button type="submit" disabled={!valid || busy || !keyId || !operation.length}>
        Test
      </Button>
      {error && (
        <p role="alert" className="text-sm text-[var(--error-text)]">
          {error}
        </p>
      )}
      {result && (
        <div
          className="grid gap-1.5 rounded-[var(--radius-card)] bg-[var(--surface-2)] p-3"
          aria-live="polite"
        >
          <DecisionChip decision={result.decision} />
          <p className="text-sm">{result.reason}</p>
          {result.matches.some((m) => m.policy_id !== result.policy?.policy_id) && (
            <p className="text-xs text-[var(--muted-foreground)]">
              Also matched:{' '}
              {result.matches
                .filter((m) => m.policy_id !== result.policy?.policy_id)
                .map((m) => `${m.name} v${m.version}`)
                .join(', ')}
            </p>
          )}
        </div>
      )}
    </form>
  )
}
