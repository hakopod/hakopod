import { useEffect, useRef, useState } from 'react'
import { Link, useNavigate } from '@tanstack/react-router'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { APIError, message } from '../lib/api'
import { client, unwrap } from '../lib/client'
import { useProjects } from '../lib/projects'
import { useScope } from '../lib/scope'
import type { components } from '../lib/api.generated'
import type { Project } from '../lib/types'
import { FormPage, FormSection } from './form-page'
import { Button } from './ui/button'
import { Input } from './ui/input'
import { SelectField } from './ui/select'
import { Textarea } from './ui/textarea'
import { Empty, ErrorState, Loading, Note, RequestError } from './shared'

// A stored DNS provider credential as the API returns it. The token is never
// returned, so the form only ever sends a replacement.
export type DNSProvider = components['schemas']['DNSProvider']
export type DNSProviderInput = components['schemas']['DNSProviderInput'] & { name: string }

// An administrator's listing carries full rows; a delegated owner's carries name
// and kind only, and that summary cannot be edited here.
export function storedDNSProviders(
  items: (DNSProvider | components['schemas']['DNSProviderSummary'])[],
) {
  return items.filter((item): item is DNSProvider => 'zone_filter' in item)
}

export type DNSProviderDraft = {
  name: string
  kind: 'cloudflare'
  project: string
  environment: string
  zones: string
  enabled: boolean
  token: string
}

export const dnsProviderKinds = { cloudflare: 'Cloudflare' } as const

export function dnsProviderDraft(provider?: DNSProvider): DNSProviderDraft {
  return {
    name: provider?.name || '',
    kind: 'cloudflare',
    project: provider?.project || '',
    environment: provider?.environment || '',
    zones: (provider?.zone_filter || []).join('\n'),
    enabled: provider ? provider.enabled : true,
    token: '',
  }
}

export function dnsProviderZones(draft: DNSProviderDraft) {
  return draft.zones.split(/[\s,]+/).filter(Boolean)
}

// prepareDNSProviderInput mirrors the server's own bounds so a mistake is
// reported next to the field rather than as a rejected request.
export function prepareDNSProviderInput(
  draft: DNSProviderDraft,
  expectedRevision: number,
  stored?: DNSProvider,
): DNSProviderInput {
  const name = draft.name.trim()
  if (!name || name.length > 80) throw new Error('Use between 1 and 80 characters for the name.')
  if ((draft.project === '') !== (draft.environment === ''))
    throw new Error('Scope this credential to both a project and an environment, or to neither.')
  const zones = dnsProviderZones(draft)
  if (zones.length < 1 || zones.length > 32)
    throw new Error('List between 1 and 32 permitted DNS zones.')
  if (new Set(zones).size !== zones.length) throw new Error('List each permitted zone once.')
  const zone = /^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$/
  const wrong = zones.find((value) => !zone.test(value))
  if (wrong)
    throw new Error(
      `Enter ${wrong} as a lowercase DNS name without a scheme, port, path or wildcard.`,
    )
  const token = draft.token.trim()
  if (token.length > 8192) throw new Error('The API token is longer than 8192 characters.')
  if (!stored && !token) throw new Error('Enter an API token for a new credential.')
  // The stored token is sealed against the name, kind and permitted zones, so a
  // change to any of those cannot carry it forward.
  if (
    stored &&
    !token &&
    (stored.name !== name ||
      stored.zone_filter.length !== zones.length ||
      stored.zone_filter.some((value, index) => value !== zones[index]))
  )
    throw new Error(
      'Changing the name or the permitted zones requires entering the API token again.',
    )
  const input: DNSProviderInput = {
    name,
    kind: draft.kind,
    zone_filter: zones,
    enabled: draft.enabled,
    expected_revision: expectedRevision,
  }
  if (draft.project) {
    input.project = draft.project
    input.environment = draft.environment
  }
  if (token) input.credentials = { token }
  return input
}

export function dnsProviderScope(provider: Pick<DNSProvider, 'project' | 'environment'>) {
  return provider.project
    ? `${provider.project} / ${provider.environment}`
    : 'All projects and environments'
}

export function DNSProviderCredentialFields({
  draft,
  setDraft,
  projects,
  stored,
}: {
  draft: DNSProviderDraft
  setDraft: (draft: DNSProviderDraft) => void
  projects: Project[]
  stored?: DNSProvider
}) {
  const environments = projects.find((item) => item.name === draft.project)?.environments || []
  return (
    <div className="grid gap-4">
      <FormSection
        title="Credential"
        description="Name this credential and choose the DNS provider that holds the zones."
      >
        <label>
          Name
          <Input
            required
            maxLength={80}
            value={draft.name}
            readOnly={Boolean(stored)}
            onChange={(event) => setDraft({ ...draft, name: event.target.value })}
            placeholder="cloudflare-production"
            spellCheck={false}
          />
          <small className="field-help">
            {stored
              ? 'The name identifies the stored token and cannot be changed here.'
              : 'Shown when someone picks a provider to create records with.'}
          </small>
        </label>
        <p className="field-help">Cloudflare is the only supported DNS provider.</p>
        <label>
          API token
          <Input
            type="password"
            maxLength={8192}
            value={draft.token}
            onChange={(event) => setDraft({ ...draft, token: event.target.value })}
            autoComplete="new-password"
            spellCheck={false}
          />
          <small className="field-help">
            {stored
              ? 'Leave this blank to keep the stored token. Stored tokens are never shown again. Changing the permitted zones or the name requires entering the token again.'
              : 'Use a token limited to editing DNS for the zones below.'}
          </small>
        </label>
      </FormSection>
      <FormSection
        title="Permitted zones"
        description="Records are only written inside these zones, whatever the token itself can reach."
      >
        <label>
          Zones
          <Textarea
            rows={4}
            required
            maxLength={4096}
            value={draft.zones}
            onChange={(event) => setDraft({ ...draft, zones: event.target.value })}
            placeholder={'example.com\nexample.net'}
            spellCheck={false}
          />
          <small className="field-help">
            One zone per line, between 1 and 32. A hostname outside every zone is refused.
          </small>
        </label>
      </FormSection>
      <FormSection
        title="Availability"
        description="Limit this credential to one project and environment, or leave it available everywhere."
      >
        {stored && (
          <p className="field-help">
            A stored credential keeps the project and environment it was created with. Add a second
            credential to cover another scope.
          </p>
        )}
        <div className="form-grid-two">
          <label>
            Project
            <SelectField
              label="Project"
              disabled={Boolean(stored)}
              value={draft.project}
              onValueChange={(value) =>
                setDraft({
                  ...draft,
                  project: value,
                  environment: value
                    ? projects.find((item) => item.name === value)?.environments[0]?.name || ''
                    : '',
                })
              }
              options={[
                { value: '', label: 'All projects' },
                ...projects.map((item) => ({ value: item.name, label: item.name })),
              ]}
            />
          </label>
          <label>
            Environment
            <SelectField
              label="Environment"
              value={draft.environment}
              disabled={Boolean(stored) || !draft.project}
              onValueChange={(value) => setDraft({ ...draft, environment: value })}
              options={
                draft.project
                  ? environments.map((item) => ({ value: item.name, label: item.name }))
                  : [{ value: '', label: 'All environments' }]
              }
            />
          </label>
        </div>
        <label className="checkbox-row">
          <Input
            type="checkbox"
            checked={draft.enabled}
            onChange={(event) => setDraft({ ...draft, enabled: event.target.checked })}
          />
          Available for creating records
        </label>
        <small className="field-help">
          A credential that is not available stays stored but cannot be chosen.
        </small>
      </FormSection>
    </div>
  )
}

export function DNSProviderReview({
  input,
  stored,
}: {
  input: DNSProviderInput
  stored?: DNSProvider
}) {
  return (
    <FormSection title="Review credential">
      <Note>
        {input.credentials
          ? 'The API token you entered replaces any stored token. hakopod cannot check what a token reaches, so the permitted zones below are the boundary it enforces.'
          : 'The stored API token is kept. hakopod cannot check what a token reaches, so the permitted zones below are the boundary it enforces.'}
      </Note>
      <dl className="service-definition-list">
        <div>
          <dt>Name</dt>
          <dd className="break-all">{input.name}</dd>
        </div>
        <div>
          <dt>Provider</dt>
          <dd>{dnsProviderKinds[input.kind as keyof typeof dnsProviderKinds] || input.kind}</dd>
        </div>
        <div>
          <dt>Available to</dt>
          <dd>{dnsProviderScope(input)}</dd>
        </div>
        <div>
          <dt>Permitted zones</dt>
          <dd className="break-all">{input.zone_filter.join(', ')}</dd>
        </div>
        <div>
          <dt>Availability</dt>
          <dd>{input.enabled ? 'Available for creating records' : 'Not available'}</dd>
        </div>
        <div>
          <dt>API token</dt>
          <dd>{input.credentials ? 'Replaced with the token you entered' : 'Stored token kept'}</dd>
        </div>
      </dl>
      {stored && (
        <dl className="service-definition-list">
          <div>
            <dt>Currently stored</dt>
            <dd className="break-all">
              {dnsProviderScope(stored)} · {stored.zone_filter.join(', ')} ·{' '}
              {stored.enabled ? 'available' : 'not available'} · revision {stored.revision}
            </dd>
          </div>
        </dl>
      )}
    </FormSection>
  )
}

export function DNSProviderEditor({
  name,
  project = '',
  environment = '',
}: {
  name?: string
  project?: string
  environment?: string
}) {
  const navigate = useNavigate()
  const cache = useQueryClient()
  const scope = useScope()
  const projects = useProjects()
  const providers = useQuery({
    queryKey: ['dns-providers'],
    queryFn: ({ signal }) => unwrap(client.GET('/dns-providers', { signal })),
    enabled: scope.identity.admin,
    gcTime: 0,
    refetchOnWindowFocus: false,
  })
  // A name is unique per scope, not installation-wide, so the scope the list
  // linked with is part of finding the row being edited.
  const stored = storedDNSProviders(providers.data?.items || []).find(
    (item) =>
      item.name === name &&
      (item.project || '') === project &&
      (item.environment || '') === environment,
  )
  const [draft, setDraft] = useState<DNSProviderDraft | null>(null)
  const [review, setReview] = useState<DNSProviderInput | null>(null)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const heading = useRef<HTMLDivElement | null>(null)
  const reviewed = useRef(false)
  useEffect(() => {
    if (review) reviewed.current = true
    if (reviewed.current) {
      heading.current?.focus()
      heading.current?.scrollIntoView({ block: 'start' })
    }
  }, [Boolean(review)])
  if (!scope.identity.admin)
    return (
      <Empty
        icon="lock"
        title="Administrator access required"
        description="DNS provider credentials are configured by installation administrators."
      />
    )
  if (projects.isPending || providers.isPending) return <Loading />
  if (projects.error)
    return <ErrorState error={projects.error} retry={() => void projects.refetch()} />
  if (providers.error)
    return <ErrorState error={providers.error} retry={() => void providers.refetch()} />
  if (name && !stored)
    return (
      <Empty
        icon="globe"
        title="Credential not found"
        description="It may have been deleted. Return to DNS providers to see the stored credentials."
      />
    )
  const values = draft || dnsProviderDraft(stored)
  async function save(input: DNSProviderInput) {
    if (busy) return
    setBusy(true)
    setError('')
    try {
      await unwrap(
        client.PUT('/dns-providers/{name}', {
          params: { path: { name: input.name } },
          body: input,
        }),
      )
      void cache.invalidateQueries({ queryKey: ['dns-providers'] })
      await navigate({ to: '/settings/dns-providers', search: { saved: input.name } })
    } catch (cause) {
      setError(
        cause instanceof APIError && cause.status === 409
          ? stored
            ? 'The credential changed after you opened it. Your entries are kept; reopen it from DNS providers to see the stored values before saving.'
            : 'A credential with this name already exists. Your entries are kept; choose another name.'
          : message(cause),
      )
      if (!stored) setReview(null)
    } finally {
      setBusy(false)
    }
  }
  return (
    <FormPage
      title={stored ? `Edit ${stored.name}` : 'Add DNS provider'}
      description="Store a provider token and the zones it may write records in."
      breadcrumbs={[
        { label: 'Settings', to: '/settings' },
        { label: 'DNS providers', to: '/settings/dns-providers' },
        { label: stored ? stored.name : 'Add credential' },
      ]}
    >
      <div
        className="muted-text mb-4 text-sm"
        ref={heading}
        tabIndex={-1}
        aria-label={review ? 'Review DNS provider' : 'Configure DNS provider'}
      >
        {review ? '2 / 2 · Review' : '1 / 2 · Configure'}
      </div>
      <form
        autoComplete="off"
        onSubmit={(event) => {
          event.preventDefault()
          if (review) {
            void save(review)
            return
          }
          setError('')
          try {
            setReview(prepareDNSProviderInput(values, stored?.revision || 0, stored))
          } catch (cause) {
            setError(message(cause))
          }
        }}
      >
        {review ? (
          <DNSProviderReview input={review} stored={stored} />
        ) : (
          <DNSProviderCredentialFields
            draft={values}
            setDraft={setDraft}
            projects={projects.data?.items || []}
            stored={stored}
          />
        )}
        {error && <RequestError error={error} />}
        <div className="form-footer">
          {review ? (
            <Button type="button" disabled={busy} onClick={() => setReview(null)}>
              Back to configuration
            </Button>
          ) : (
            <Button asChild>
              <Link to="/settings/dns-providers" search={{}}>
                Cancel
              </Link>
            </Button>
          )}
          <Button type="submit" variant="primary" disabled={busy}>
            {busy
              ? 'Saving…'
              : review
                ? stored
                  ? 'Save credential'
                  : 'Create credential'
                : 'Review credential'}
          </Button>
        </div>
      </form>
    </FormPage>
  )
}
