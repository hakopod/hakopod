// Pure helpers for the agent policy editor. Rules mirror internal/agentaccess/document.go.
import type { AgentPolicyDocument } from './agent-policies'

type Field = AgentPolicyDocument['when'][number]['field']

export const policyFields: Field[] = [
  'category',
  'operation',
  'environment',
  'project',
  'team',
  'key',
]

// What each field means to someone writing a rule, shown in the field picker.
export const fieldHints: Record<Field, string> = {
  category: 'A kind of action, such as deployments or credentials',
  operation: 'One API operation, or a pattern such as delete*',
  environment: 'Where the request lands, such as production',
  project: 'The project the request acts on',
  team: 'A team the key owner belongs to',
  key: 'A specific API key',
}

export const categoryLabels: Record<string, string> = {
  deploy: 'Deployments',
  write: 'Configuration changes',
  credentials: 'Credentials and secrets',
  terminal: 'Terminal and command execution',
  backup: 'Backups and restores',
  admin: 'Installation administration',
  read: 'Read-only requests',
}
export const policyCategories = Object.keys(categoryLabels)

// createDeployment -> Create deployment
export function humanizeOperation(id: string) {
  const words = id.replace(/([a-z0-9])([A-Z])/g, '$1 $2').toLowerCase()
  return words.charAt(0).toUpperCase() + words.slice(1)
}

export type PolicyTemplate = {
  id: string
  title: string
  detail: string
  name: string
  document: AgentPolicyDocument
}

// Starting points for a new policy. Each one is a valid document.
export const policyTemplates: PolicyTemplate[] = [
  {
    id: 'production-deploys',
    title: 'Approve production deploys',
    detail: 'A person approves each deployment to production before it runs.',
    name: 'Production deploys need approval',
    document: {
      schema_version: 1,
      description: 'Deployments to production wait for a project administrator.',
      when: [
        { field: 'category', operator: 'is_any_of', values: ['deploy'] },
        { field: 'environment', operator: 'is_any_of', values: ['production'] },
      ],
      then: {
        effect: 'require_approval',
        approvers: [{ kind: 'project_admins' }],
        approval_ttl_seconds: 900,
      },
      except: { active_incident: true },
    },
  },
  {
    id: 'credentials',
    title: 'Approve credential access',
    detail: 'Revealing database passwords or creating keys needs a person to approve.',
    name: 'Credential access needs approval',
    document: {
      schema_version: 1,
      description: 'Credential reads and new keys wait for approval; new keys expire within a day.',
      when: [{ field: 'category', operator: 'is_any_of', values: ['credentials'] }],
      then: {
        effect: 'require_approval',
        approvers: [{ kind: 'project_admins' }],
        approval_ttl_seconds: 900,
        max_credential_ttl_seconds: 86400,
      },
    },
  },
  {
    id: 'deletes',
    title: 'Approve deletions',
    detail: 'Deleting applications, databases or other resources waits for approval.',
    name: 'Deletions need approval',
    document: {
      schema_version: 1,
      description: 'Any delete operation waits for a project administrator.',
      when: [{ field: 'operation', operator: 'matches', values: ['delete*'] }],
      then: {
        effect: 'require_approval',
        approvers: [{ kind: 'project_admins' }],
        approval_ttl_seconds: 3600,
      },
    },
  },
  {
    id: 'terminal',
    title: 'Block terminal access',
    detail: 'API keys cannot open terminals or run commands in containers.',
    name: 'No terminal access for keys',
    document: {
      schema_version: 1,
      description: 'Terminal and command execution are refused for API keys.',
      when: [{ field: 'category', operator: 'is_any_of', values: ['terminal'] }],
      then: { effect: 'block' },
    },
  },
]
export const approvalTTLOptions = [
  { value: '300', label: '5 min' },
  { value: '900', label: '15 min' },
  { value: '3600', label: '1 h' },
  { value: '14400', label: '4 h' },
  { value: '86400', label: '24 h' },
]
export const credentialTTLOptions = [
  { value: '', label: 'Not limited' },
  { value: '900', label: '15 min' },
  { value: '3600', label: '1 h' },
  { value: '86400', label: '1 day' },
  { value: '604800', label: '7 days' },
  { value: '2592000', label: '30 days' },
]

export type PolicyErrors = Record<string, string>

// Keys: name, description, when, when.<i>, then, approvers, approval_ttl, credential_ttl.
export function validatePolicy(name: string, doc: AgentPolicyDocument): PolicyErrors {
  const errors: PolicyErrors = {}
  if (!name.trim()) errors.name = 'Enter a policy name.'
  if (doc.description.length > 200) errors.description = 'Use 200 characters or fewer.'
  if (doc.when.length < 1 || doc.when.length > 8) errors.when = 'Use between 1 and 8 conditions.'
  const seen = new Set<Field>()
  doc.when.forEach((condition, index) => {
    const key = `when.${index}`
    if (seen.has(condition.field)) errors[key] = 'Each field can be used once.'
    else if (condition.values.length < 1) errors[key] = 'Add at least one value.'
    else if (condition.values.length > 16) errors[key] = 'Use 16 values or fewer.'
    else if (condition.values.some((value) => !value.trim() || value.length > 128))
      errors[key] = 'Values must be 1 to 128 characters.'
    else if (condition.operator !== 'matches' && condition.values.some((v) => v.includes('*')))
      errors[key] = 'Use "matches" for values with *.'
    seen.add(condition.field)
  })
  const then = doc.then
  const approvers = then.approvers ?? []
  if (then.effect === 'block' && approvers.length) errors.approvers = 'Blocking takes no approvers.'
  if (approvers.length > 8) errors.approvers = 'Use 8 approvers or fewer.'
  if (approvers.some((a) => a.kind !== 'project_admins' && !a.id))
    errors.approvers = 'Choose who approves.'
  const ttl = then.approval_ttl_seconds
  if (then.effect === 'require_approval' && (!ttl || ttl < 300 || ttl > 86400))
    errors.approval_ttl = 'Choose between 5 minutes and 24 hours.'
  const cap = then.max_credential_ttl_seconds
  if (cap !== undefined && (cap < 300 || cap > 2592000))
    errors.credential_ttl = 'Choose between 5 minutes and 30 days.'
  return errors
}

const categoryPhrases: Record<string, string> = {
  deploy: 'a deployment',
  write: 'a configuration change',
  credentials: 'credential access',
  terminal: 'terminal access',
  backup: 'a backup or restore',
  admin: 'installation administration',
  read: 'a read-only request',
}

function list(values: string[], join: string) {
  return values.length <= 1
    ? values.join('')
    : `${values.slice(0, -1).join(', ')} ${join} ${values[values.length - 1]}`
}

export function durationLabel(seconds: number) {
  return (
    [...approvalTTLOptions, ...credentialTTLOptions].find((o) => o.value === String(seconds))
      ?.label ?? `${Math.round(seconds / 60)} min`
  )
}

function condition(c: AgentPolicyDocument['when'][number], name: (id: string) => string) {
  const not = c.operator === 'is_none_of'
  const values =
    c.field === 'team' || c.field === 'key'
      ? c.values.map(name)
      : c.field === 'operation' && c.operator !== 'matches'
        ? c.values.map(humanizeOperation)
        : c.values
  switch (c.field) {
    case 'category':
      return `the request is ${not ? 'not ' : ''}${list(
        c.values.map((v) => categoryPhrases[v] ?? v),
        'or',
      )}`
    case 'operation':
      return c.operator === 'matches'
        ? `the operation matches ${list(values, 'or')}`
        : `the operation is ${not ? 'not ' : ''}${list(values, 'or')}`
    case 'team':
      return `the key owner is ${not ? 'not ' : ''}in ${list(values, 'or')}`
    default:
      return `the ${c.field} ${c.operator === 'matches' ? 'matches' : not ? 'is not' : 'is'} ${list(values, 'or')}`
  }
}

// Plain-English description of a policy. `name` maps ids (teams, keys, people) to labels.
export function summarizePolicy(
  doc: AgentPolicyDocument,
  name: (id: string) => string = (id) => id,
) {
  const parts = doc.when.filter((c) => c.values.length > 0).map((c) => condition(c, name))
  const when = parts.length ? `When ${list(parts, 'and')}` : 'When a request matches'
  let then: string
  if (doc.then.effect === 'block') then = 'refuse it.'
  else {
    const approvers = (doc.then.approvers ?? []).map((a) =>
      a.kind === 'project_admins' ? 'project administrators' : name(a.id ?? ''),
    )
    then = `hold it for approval by ${approvers.length ? list(approvers, 'or') : 'an installation administrator'}. Unapproved requests expire after ${durationLabel(doc.then.approval_ttl_seconds ?? 0)}.`
  }
  const cap = doc.then.max_credential_ttl_seconds
    ? ` Keys it creates must expire within ${durationLabel(doc.then.max_credential_ttl_seconds)}.`
    : ''
  const except = doc.except?.active_incident
    ? ' While an alarm is active, matching requests run and a notice is sent.'
    : ''
  return `${when}, ${then}${cap}${except}`
}
