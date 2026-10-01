import { useId, useState } from 'react'
import {
  newEdgeRule,
  type EdgeDraft,
  type EdgePolicy,
  type EdgeRule,
  type EdgeRuleDraft,
} from '../lib/proxy-settings'
import { FormSection } from './form-page'
import { Icon } from './icons'
import { InstallationReviewRows } from './installation-form-fields'
import { Note } from './shared'
import { Button } from './ui/button'
import { Input } from './ui/input'
import { SelectField } from './ui/select'
import { Textarea } from './ui/textarea'

export function EdgePolicyFields({
  draft,
  onChange,
}: {
  draft: EdgeDraft
  onChange: (value: EdgeDraft) => void
}) {
  const sourceHelp = useId()
  const trustedHelp = useId()
  const countryHelp = useId()
  const [newRuleKey, setNewRuleKey] = useState<string | null>(null)
  const trusted = draft.client_ip_source === 'trusted_proxy'
  const change = <K extends keyof EdgeDraft>(key: K, value: EdgeDraft[K]) =>
    onChange({ ...draft, [key]: value })
  function move(index: number, offset: number) {
    const rules = [...draft.rules]
    const target = index + offset
    if (target < 0 || target >= rules.length) return
    ;[rules[index], rules[target]] = [rules[target], rules[index]]
    change('rules', rules)
  }
  return (
    <>
      <FormSection
        title="Traffic protection"
        description="Control HTTP access and per-client request rates at the installation ingress."
      >
        <label className="checkbox-label">
          <Input
            type="checkbox"
            checked={draft.enabled}
            onChange={(event) => change('enabled', event.target.checked)}
          />
          Enable traffic protection
        </label>
        {!draft.enabled && (
          <Note>Protection is disabled. Rules are kept and can be edited before enabling.</Note>
        )}
        <label className="grid gap-2">
          Client IP source
          <SelectField
            label="Client IP source"
            value={draft.client_ip_source}
            onValueChange={(value) =>
              onChange({
                ...draft,
                client_ip_source: value as EdgeDraft['client_ip_source'],
                client_ip_header:
                  value === 'trusted_proxy'
                    ? draft.client_ip_header || 'CF-Connecting-IP'
                    : draft.client_ip_header,
              })
            }
            options={[
              { value: 'connection', label: 'Direct connection' },
              { value: 'trusted_proxy', label: 'Trusted proxy header' },
            ]}
            aria-describedby={sourceHelp}
          />
          <small id={sourceHelp} className="field-help">
            {trusted
              ? 'Use this when a trusted CDN or proxy supplies the original client address.'
              : 'Use the address HAProxy receives. Forwarded client and country headers are ignored.'}
          </small>
        </label>
        {trusted && (
          <>
            <label className="grid gap-2">
              Trusted proxy networks
              <Textarea
                value={draft.trusted_proxy_cidrs}
                onChange={(event) => change('trusted_proxy_cidrs', event.target.value)}
                rows={3}
                maxLength={2048}
                required
                spellCheck={false}
                className="font-mono text-sm"
                aria-describedby={trustedHelp}
              />
              <small id={trustedHelp} className="field-help">
                Enter up to 32 proxy IPs or CIDRs, one per line. Use the current networks published
                by your proxy provider; /0 is not allowed.
              </small>
            </label>
            <div className="grid gap-4 sm:grid-cols-2">
              <label className="grid gap-2">
                Client IP header
                <SelectField
                  label="Client IP header"
                  value={draft.client_ip_header}
                  onValueChange={(value) =>
                    change('client_ip_header', value as EdgeDraft['client_ip_header'])
                  }
                  required
                  options={[
                    { value: '', label: 'Choose a header' },
                    { value: 'CF-Connecting-IP', label: 'CF-Connecting-IP' },
                    { value: 'X-Real-IP', label: 'X-Real-IP' },
                  ]}
                />
              </label>
              <label className="grid gap-2">
                Country header
                <SelectField
                  label="Country header"
                  value={draft.country_header}
                  onValueChange={(value) =>
                    change('country_header', value as EdgeDraft['country_header'])
                  }
                  options={[
                    { value: '', label: 'No country filtering' },
                    { value: 'CF-IPCountry', label: 'CF-IPCountry' },
                    { value: 'CloudFront-Viewer-Country', label: 'CloudFront-Viewer-Country' },
                  ]}
                  aria-describedby={countryHelp}
                />
              </label>
            </div>
            <p id={countryHelp} className="field-help">
              Country filtering uses the location asserted by your trusted proxy. Hakopod does not
              look up IP locations.
            </p>
            <Note>
              Protected routes reject direct connections and requests from outside these proxy
              networks. Missing or invalid client headers are rejected. Country rules also reject
              missing or invalid country headers. Configure the proxy to replace visitor-supplied
              headers.
            </Note>
          </>
        )}
      </FormSection>
      <FormSection title="Traffic rules">
        <Note>
          The first matching hostname and path rule wins. Put specific paths before broader ones.
          Later rules do not add restrictions to a match. Requests with no matching rule keep their
          existing access, but listed hosts reject ambiguous paths such as /a/../b or encoded
          separators. Use canonical URLs. Certificate-challenge token paths remain available.
        </Note>
        <div className="grid min-w-0 gap-5">
          {draft.rules.map((rule, index) => (
            <RuleFields
              key={rule.key}
              rule={rule}
              index={index}
              total={draft.rules.length}
              countryAvailable={trusted && Boolean(draft.country_header)}
              initiallyOpen={rule.key === newRuleKey}
              onChange={(next) =>
                change(
                  'rules',
                  draft.rules.map((item, i) => (i === index ? next : item)),
                )
              }
              onMove={(offset) => move(index, offset)}
              onRemove={() =>
                change(
                  'rules',
                  draft.rules.filter((_, i) => i !== index),
                )
              }
            />
          ))}
          {!draft.rules.length && (
            <p className="m-0 text-sm text-muted-foreground">
              No traffic rules. Add a hostname to protect it.
            </p>
          )}
        </div>
        <div className="flex flex-wrap items-center gap-3">
          <Button
            type="button"
            disabled={draft.rules.length >= 32}
            onClick={() => {
              const rule = newEdgeRule(draft.rules)
              setNewRuleKey(rule.key)
              change('rules', [...draft.rules, rule])
            }}
          >
            <Icon name="plus" size={15} />
            Add rule
          </Button>
          <span className="text-xs text-muted-foreground">{draft.rules.length} of 32 rules</span>
        </div>
      </FormSection>
    </>
  )
}

function RuleFields({
  rule,
  index,
  total,
  countryAvailable,
  initiallyOpen,
  onChange,
  onMove,
  onRemove,
}: {
  rule: EdgeRuleDraft
  index: number
  total: number
  countryAvailable: boolean
  initiallyOpen: boolean
  onChange: (value: EdgeRuleDraft) => void
  onMove: (offset: number) => void
  onRemove: () => void
}) {
  const ruleHeading = useId()
  const idHelp = useId(),
    hostHelp = useId(),
    pathHelp = useId(),
    rateHelp = useId()
  const allowHelp = useId(),
    denyHelp = useId(),
    countryHelp = useId()
  const change = <K extends keyof EdgeRuleDraft>(key: K, value: EdgeRuleDraft[K]) =>
    onChange({ ...rule, [key]: value })
  const label = rule.id || String(index + 1)
  const [open, setOpen] = useState(index === 0 || initiallyOpen)
  return (
    <details
      className="min-w-0 border-t border-border pt-4"
      open={open}
      onToggle={(event) => setOpen(event.currentTarget.open)}
    >
      <summary id={ruleHeading} className="min-w-0 cursor-pointer text-sm font-medium">
        Rule {index + 1}: {rule.id || 'Unnamed rule'}
        {rule.host && (
          <span className="ml-2 break-all font-mono text-xs text-muted-foreground">
            {rule.host}
            {rule.path_prefix || '/'}
          </span>
        )}
      </summary>
      <div className="mt-4 grid min-w-0 gap-4">
        <div className="flex min-w-0 flex-wrap items-center justify-end gap-2">
          <div className="flex flex-wrap gap-2">
            <Button
              type="button"
              size="sm"
              disabled={index === 0}
              aria-label={`Move rule ${label} earlier`}
              onClick={() => onMove(-1)}
            >
              Move up
            </Button>
            <Button
              type="button"
              size="sm"
              disabled={index === total - 1}
              aria-label={`Move rule ${label} later`}
              onClick={() => onMove(1)}
            >
              Move down
            </Button>
            <Button
              type="button"
              size="sm"
              variant="danger"
              aria-label={`Remove rule ${label}`}
              onClick={onRemove}
            >
              <Icon name="trash" size={14} />
              Remove
            </Button>
          </div>
        </div>
        <div className="grid gap-4 sm:grid-cols-2">
          <label className="grid gap-2">
            Rule ID
            <Input
              required
              value={rule.id}
              maxLength={32}
              pattern="[a-z0-9][a-z0-9_\-]{0,31}"
              spellCheck={false}
              onChange={(event) => change('id', event.target.value)}
              aria-describedby={idHelp}
            />
            <small id={idHelp} className="field-help">
              Unique lowercase name using letters, digits, - or _.
            </small>
          </label>
          <label className="grid gap-2">
            Hostname
            <Input
              required
              value={rule.host}
              maxLength={253}
              placeholder="app.example.com"
              spellCheck={false}
              onChange={(event) => change('host', event.target.value)}
              aria-describedby={hostHelp}
            />
            <small id={hostHelp} className="field-help">
              Exact hostname, without a scheme, port or wildcard.
            </small>
          </label>
          <label className="grid gap-2">
            Path prefix
            <Input
              required
              value={rule.path_prefix}
              maxLength={128}
              placeholder="/"
              spellCheck={false}
              onChange={(event) => change('path_prefix', event.target.value)}
              aria-describedby={pathHelp}
            />
            <small id={pathHelp} className="field-help">
              Use / for all paths or a prefix such as /api. No query string or encoded characters.
            </small>
          </label>
          <label className="grid gap-2">
            Requests per second
            <Input
              required
              type="number"
              min={0}
              max={100000}
              step={1}
              value={rule.requests_per_second}
              onChange={(event) => change('requests_per_second', event.target.value)}
              aria-describedby={rateHelp}
            />
            <small id={rateHelp} className="field-help">
              Per client, for this rule. 0 disables the limit. Each HAProxy process and listener
              counts separately.
            </small>
          </label>
          <label className="grid gap-2">
            Allowed IPs and networks
            <Textarea
              value={rule.allow_cidrs}
              rows={3}
              maxLength={4096}
              spellCheck={false}
              className="font-mono text-sm"
              onChange={(event) => change('allow_cidrs', event.target.value)}
              aria-describedby={allowHelp}
            />
            <small id={allowHelp} className="field-help">
              Up to 64 IPs or CIDRs, one per line. Empty allows any client address.
            </small>
          </label>
          <label className="grid gap-2">
            Denied IPs and networks
            <Textarea
              value={rule.deny_cidrs}
              rows={3}
              maxLength={4096}
              spellCheck={false}
              className="font-mono text-sm"
              onChange={(event) => change('deny_cidrs', event.target.value)}
              aria-describedby={denyHelp}
            />
            <small id={denyHelp} className="field-help">
              Up to 64 IPs or CIDRs, one per line. Denials take precedence over allowed entries.
            </small>
          </label>
          <label className="grid gap-2">
            Allowed country codes
            <Input
              value={rule.allow_countries}
              maxLength={256}
              placeholder="US, DE"
              spellCheck={false}
              onChange={(event) => change('allow_countries', event.target.value)}
              aria-describedby={countryHelp}
            />
          </label>
          <label className="grid gap-2">
            Denied country codes
            <Input
              value={rule.deny_countries}
              maxLength={256}
              spellCheck={false}
              onChange={(event) => change('deny_countries', event.target.value)}
              aria-describedby={countryHelp}
            />
          </label>
        </div>
        <p id={countryHelp} className="field-help">
          {countryAvailable
            ? 'Use up to 64 two-letter country codes per list. Empty allow lists allow any country. If you set both IP and country allow lists, a client must match both. Denials take precedence.'
            : 'Country lists require a trusted proxy and country header above. Leave both lists empty for direct connections.'}
        </p>
      </div>
    </details>
  )
}

export function EdgePolicySummary({
  policy,
  compact = false,
}: {
  policy: EdgePolicy
  compact?: boolean
}) {
  return (
    <div className="grid min-w-0 gap-4">
      <InstallationReviewRows
        rows={[
          ['Protection', policy.enabled ? 'Enabled' : 'Disabled; rules retained'],
          [
            'Client IP source',
            policy.client_ip_source === 'connection'
              ? 'Direct connection'
              : `Trusted proxy · ${policy.client_ip_header}`,
          ],
          ...(policy.client_ip_source === 'trusted_proxy'
            ? ([
                ['Trusted proxies', (policy.trusted_proxy_cidrs || []).join(', ')],
                ['Country source', policy.country_header || 'No country filtering'],
              ] as [string, string][])
            : []),
        ]}
      />
      {policy.rules.length ? (
        <ol
          className="m-0 grid min-w-0 list-none gap-4 p-0"
          aria-label="Traffic rules in evaluation order"
        >
          {policy.rules.map((rule, index) => (
            <li key={rule.id} className="grid min-w-0 gap-2 border-t border-border pt-3">
              {compact ? (
                <details>
                  <summary className="min-w-0 text-sm">
                    <strong className="break-words font-medium">
                      {index + 1}. {rule.id}
                    </strong>
                    <span className="mt-1 block break-all font-mono text-xs">
                      {rule.host}
                      {rule.path_prefix || '/'}
                    </span>
                    <span className="mt-1 block text-xs text-muted-foreground">
                      {ruleDescription(rule)}
                    </span>
                  </summary>
                  <div className="mt-3">
                    <RuleValues rule={rule} />
                  </div>
                </details>
              ) : (
                <>
                  <strong className="break-words text-sm font-medium">
                    {index + 1}. {rule.id}
                  </strong>
                  <RuleValues rule={rule} />
                </>
              )}
            </li>
          ))}
        </ol>
      ) : (
        <p className="m-0 text-sm text-muted-foreground">No traffic rules.</p>
      )}
    </div>
  )
}

function ruleDescription(rule: EdgeRule): string {
  return (
    [
      rule.allow_cidrs?.length && `Allowed IPs: ${rule.allow_cidrs.length}`,
      rule.deny_cidrs?.length && `Denied IPs: ${rule.deny_cidrs.length}`,
      rule.allow_countries?.length && `Allowed countries: ${rule.allow_countries.length}`,
      rule.deny_countries?.length && `Denied countries: ${rule.deny_countries.length}`,
      rule.requests_per_second && `${rule.requests_per_second} requests/s`,
    ]
      .filter(Boolean)
      .join(' · ') || 'No access or rate restrictions'
  )
}

function RuleValues({ rule }: { rule: EdgeRule }) {
  return (
    <InstallationReviewRows
      rows={[
        [
          'Route',
          <code className="break-all">
            {rule.host}
            {rule.path_prefix || '/'}
          </code>,
        ],
        ['Allowed IPs', (rule.allow_cidrs || []).join(', ') || 'Any'],
        ['Denied IPs', (rule.deny_cidrs || []).join(', ') || 'None'],
        ['Allowed countries', (rule.allow_countries || []).join(', ') || 'Any'],
        ['Denied countries', (rule.deny_countries || []).join(', ') || 'None'],
        [
          'Rate limit',
          rule.requests_per_second
            ? `${rule.requests_per_second} requests/s per client, process and listener`
            : 'Disabled',
        ],
      ]}
    />
  )
}
