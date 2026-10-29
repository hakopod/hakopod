import { useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { client, unwrap } from '../lib/client'
import { APIError, message } from '../lib/api'
import type { components } from '../lib/api.generated'
import { InstallationAccess } from './installation-access'
import { InstallationReviewRows } from './installation-form-fields'
import { Button } from './ui/button'
import { Field } from './ui/field'
import { SelectField } from './ui/select'
import { Card } from './ui/surfaces'
import { Textarea } from './ui/textarea'
import { ErrorState, HeadingHelp, Loading, Note, RequestError } from './shared'

type Access = components['schemas']['InstallationAccess']
type Policy = components['schemas']['InstallationAccessPolicy']

const lines = (value: string) =>
  value
    .split(/[\s,]+/)
    .map((item) => item.trim())
    .filter(Boolean)

function useAccessPolicy() {
  return useQuery({
    queryKey: ['installation-access'],
    queryFn: ({ signal }) => unwrap(client.GET('/installation/access', { signal })),
    refetchInterval: (query) => (query.state.data?.pending_until ? 5000 : false),
    refetchOnWindowFocus: false,
    retry: false,
  })
}

export function AccessPolicyPanel() {
  return (
    <InstallationAccess managedDescription="Management network access is run by the Cloud service.">
      <AccessPolicyLoader />
    </InstallationAccess>
  )
}

function AccessPolicyLoader() {
  const query = useAccessPolicy()
  if (query.isPending) return <Loading />
  if (query.error || !query.data)
    return <ErrorState error={query.error} retry={() => void query.refetch()} />
  return <AccessPolicyForm key={query.data.revision} current={query.data} />
}

function AccessPolicyForm({ current }: { current: Access }) {
  const cache = useQueryClient()
  const policy = current.policy
  const [enabled, setEnabled] = useState(policy.enabled)
  const [allow, setAllow] = useState(policy.allow_cidrs.join('\n'))
  const [source, setSource] = useState<Policy['client_ip_source']>(
    policy.client_ip_source || 'connection',
  )
  const [proxies, setProxies] = useState(policy.trusted_proxy_cidrs.join('\n'))
  const [header, setHeader] = useState<Policy['client_ip_header']>(policy.client_ip_header || '')
  const [elsewhere, setElsewhere] = useState(false)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const pending = current.pending_until ? new Date(current.pending_until) : null

  async function run(action: () => Promise<Access>) {
    if (busy) return
    setBusy(true)
    setError('')
    try {
      cache.setQueryData(['installation-access'], await action())
    } catch (cause) {
      setError(message(cause))
      if (cause instanceof APIError && cause.status === 409)
        void cache.invalidateQueries({ queryKey: ['installation-access'] })
    } finally {
      setBusy(false)
    }
  }
  const save = () =>
    run(() =>
      unwrap(
        client.PUT('/installation/access', {
          body: {
            expected_revision: current.revision,
            confirm_from_elsewhere: elsewhere,
            policy: {
              enabled,
              allow_cidrs: lines(allow),
              client_ip_source: source,
              trusted_proxy_cidrs: source === 'trusted_proxy' ? lines(proxies) : [],
              client_ip_header: source === 'trusted_proxy' ? header : '',
            },
          },
        }),
      ),
    )
  const confirm = () =>
    run(() =>
      unwrap(
        client.POST('/installation/access/confirm', { body: { revision: current.revision } }),
      ),
    )

  return (
    <div className="grid gap-4">
      <div className="hako-section-heading-title">
        <h2>Network access</h2>
        <HeadingHelp title="Network access">
          Only the networks you list can reach the dashboard, the API, the CLI and MCP. Signed
          GitHub and GitLab webhooks stay reachable. SSH to the server always works.
        </HeadingHelp>
      </div>
      {!current.licensed && (
        <Note>Network access restrictions need a Pro license. You can still turn them off.</Note>
      )}
      {pending && (
        <Card className="grid gap-3 p-4" role="status">
          <strong>Confirm the new policy</strong>
          <p className="m-0 text-sm">
            It reverts to the previous policy at {pending.toLocaleTimeString()} unless you confirm
            it from a network it allows. Reaching this page through it is the proof.
          </p>
          <div>
            <Button size="sm" variant="primary" disabled={busy} onClick={() => void confirm()}>
              {busy ? 'Confirming…' : 'Keep this policy'}
            </Button>
          </div>
        </Card>
      )}
      <Card className="grid gap-4 p-4">
        <InstallationReviewRows
          rows={[
            ['Policy', policy.enabled ? 'Restricted to listed networks' : 'Every network'],
            ['Your address', current.client_ip || 'Unknown'],
            ['Revision', String(current.revision)],
          ]}
        />
        <form
          className="grid gap-4"
          aria-label="Network access policy"
          onSubmit={(event) => {
            event.preventDefault()
            void save()
          }}
        >
          <label className="flex items-center gap-2 text-sm">
            <input
              type="checkbox"
              checked={enabled}
              onChange={(event) => setEnabled(event.target.checked)}
            />
            Restrict management access to the networks below
          </label>
          <div className="field">
            <div className="field-heading">
              <label htmlFor="access-allow">Allowed networks</label>
            </div>
            <Textarea
              id="access-allow"
              rows={5}
              value={allow}
              placeholder={'10.8.0.0/24\n203.0.113.0/24'}
              onChange={(event) => setAllow(event.target.value)}
            />
            <div className="field-help">
              One CIDR per line, IPv4 or IPv6.
              {current.client_ip && (
                <>
                  {' '}
                  <Button
                    type="button"
                    size="sm"
                    variant="outline"
                    onClick={() =>
                      setAllow((old) =>
                        [
                          ...lines(old),
                          `${current.client_ip}/${current.client_ip.includes(':') ? 128 : 32}`,
                        ].join('\n'),
                      )
                    }
                  >
                    Add {current.client_ip}
                  </Button>
                </>
              )}
            </div>
          </div>
          <SelectField
            label="Client address"
            value={source}
            onValueChange={(value) => setSource(value as Policy['client_ip_source'])}
            options={[
              { value: 'connection', label: 'Connection address' },
              { value: 'trusted_proxy', label: 'Header from a trusted proxy or load balancer' },
            ]}
          />
          {source === 'trusted_proxy' && (
            <>
              <SelectField
                label="Client address header"
                value={header}
                onValueChange={(value) => setHeader(value as Policy['client_ip_header'])}
                options={[
                  { value: 'X-Forwarded-For', label: 'X-Forwarded-For' },
                  { value: 'X-Real-IP', label: 'X-Real-IP' },
                  { value: 'CF-Connecting-IP', label: 'CF-Connecting-IP' },
                ]}
              />
              <Field
                label="Trusted proxy networks"
                value={proxies}
                onChange={(event) => setProxies(event.target.value)}
                help="The header is read only from these addresses, space or comma separated."
              />
            </>
          )}
          <label className="flex items-center gap-2 text-sm">
            <input
              type="checkbox"
              checked={elsewhere}
              onChange={(event) => setElsewhere(event.target.checked)}
            />
            Save even if it blocks my current network; I will confirm from an allowed one
          </label>
          <p className="field-help m-0">
            A saved policy reverts after five minutes unless you confirm it. If you are locked out,
            run <code>hakopod-server access-reset</code> on the server with the API environment; the
            Management network access guide has the exact command.
          </p>
          {error && <RequestError error={error} />}
          <div>
            <Button type="submit" size="sm" variant="primary" disabled={busy}>
              {busy ? 'Saving…' : 'Save policy'}
            </Button>
          </div>
        </form>
      </Card>
    </div>
  )
}
