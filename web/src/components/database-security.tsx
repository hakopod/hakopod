import { useState } from 'react'
import { Link } from '@tanstack/react-router'
import type { ManagedDatabase } from '../lib/databases'
import { client, unwrap } from '../lib/client'
import { message, timestamp } from '../lib/api'
import { Button } from './ui/button'
import { Copy, HeadingHelp, Status } from './shared'
import { canAccess, useScope } from '../lib/scope'

export function DatabaseSecurity({ database: d, now }: { database: ManagedDatabase; now: number }) {
  const { identity } = useScope()
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const tls = d.observation.tls
  const checked = Date.parse(tls?.checked_at || '')
  const fresh =
    d.observation.revision === d.revision &&
    Number.isFinite(checked) &&
    checked <= now &&
    now - checked <= 30_000
  const required = d.spec.tls?.mode === 'required'
  const expires = Date.parse(tls?.expires_at || '')
  const expired = required && Number.isFinite(expires) && expires <= now
  const verified = required && fresh && tls?.verified && tls.plaintext_rejected && expires > now
  const canManage = !identity.application && canAccess(identity, d.project, 'deployments:write')
  return (
    <section className="db-panel" aria-label="Connection security">
      <div className="db-panel-heading">
        <div>
          <h2>Connection security</h2>
          <HeadingHelp title="connection security">
            Verification checks the certificate issuer, endpoint hostname and validity, then tests
            an encrypted connection and rejects plaintext. Download contains only the public CA.
          </HeadingHelp>
        </div>
        <Status
          value={
            expired
              ? 'expired'
              : verified
                ? tls?.message
                  ? 'warning'
                  : 'verified'
                : required
                  ? 'pending'
                  : 'unenforced'
          }
        />
      </div>
      {!required ? (
        <p className="db-inline-notice">
          Client TLS is not enforced for this database. Recover into a database with required TLS
          and review application connections before cutover.
        </p>
      ) : !verified || tls?.message ? (
        <p className="db-inline-notice" role="status">
          {expired
            ? 'The last observed server certificate has expired. Check certificate renewal and verify the endpoint before connecting.'
            : tls?.message ||
              (tls?.checked_at && !fresh
                ? 'TLS verification is stale. Waiting for a current connection check.'
                : 'Waiting for verified TLS and plaintext rejection checks.')}
        </p>
      ) : null}
      <dl className="db-facts px-4 py-3">
        <div>
          <dt>Client policy</dt>
          <dd>{required ? 'TLS required' : 'Legacy policy'}</dd>
        </div>
        <div>
          <dt>Plaintext connections</dt>
          <dd>{verified ? 'Rejected' : 'Not currently verified'}</dd>
        </div>
        <div>
          <dt>Minimum protocol</dt>
          <dd>{tls?.minimum_version || 'Not observed'}</dd>
        </div>
        <div>
          <dt>Last checked</dt>
          <dd>{tls?.checked_at ? timestamp(tls.checked_at) : 'Not checked'}</dd>
        </div>
        <div>
          <dt>Certificate issuer</dt>
          <dd className="break-all">{tls?.issuer || 'Not observed'}</dd>
        </div>
        <div>
          <dt>Certificate expires</dt>
          <dd>{tls?.expires_at ? timestamp(tls.expires_at) : 'Not observed'}</dd>
        </div>
        {tls?.dns_names?.length ? (
          <div className="col-span-full">
            <dt>Certificate names</dt>
            <dd className="break-all">{tls.dns_names.join(', ')}</dd>
          </div>
        ) : null}
        {tls?.ca_fingerprint ? (
          <div className="col-span-full">
            <dt>CA fingerprint · SHA-256</dt>
            <dd className="flex items-start gap-2">
              <code className="min-w-0 break-all">{tls.ca_fingerprint}</code>
              <Copy iconOnly value={tls.ca_fingerprint} label="Copy CA fingerprint" />
            </dd>
          </div>
        ) : null}
        {tls?.fingerprint ? (
          <div className="col-span-full">
            <dt>Server fingerprint · SHA-256</dt>
            <dd className="flex items-start gap-2">
              <code className="min-w-0 break-all">{tls.fingerprint}</code>
              <Copy iconOnly value={tls.fingerprint} label="Copy server fingerprint" />
            </dd>
          </div>
        ) : null}
      </dl>
      {required && (
        <div className="px-4 pb-4">
          <div className="flex flex-wrap gap-2">
            <Button
              disabled={busy}
              onClick={async () => {
                setBusy(true)
                setError('')
                try {
                  const trust = await unwrap(
                    client.GET('/databases/{id}/trust', { params: { path: { id: d.id } } }),
                  )
                  const url = URL.createObjectURL(
                    new Blob([trust.certificate_pem], { type: 'application/x-pem-file' }),
                  )
                  const link = document.createElement('a')
                  link.href = url
                  link.download = `${d.spec.name}-ca.crt`
                  link.click()
                  setTimeout(() => URL.revokeObjectURL(url), 1000)
                } catch (err) {
                  setError(message(err))
                } finally {
                  setBusy(false)
                }
              }}
            >
              {busy ? 'Downloading…' : 'Download public CA'}
            </Button>
            {d.spec.engine === 'postgresql' && canManage && (
              <Button asChild>
                <Link
                  to="/databases/$databaseId/public-endpoints"
                  params={{ databaseId: d.id }}
                  search={{ project: d.project, environment: d.environment }}
                >
                  Public endpoints
                </Link>
              </Button>
            )}
          </div>
          {d.spec.engine === 'vitess' && (
            <p className="mt-3 text-sm text-muted-foreground">
              vtgate accepts MySQL TLS on port 3306. Use app@primary{d.spec.replicas > 0 ? ' or app@replica' : ''} as the database
              target, with the mounted public CA and verified hostname.{d.spec.replicas > 0 ? ' Replica reads may lag.' : ''} Tablets, topology members
              and control services do not expose application credentials or cluster access.
            </p>
          )}
          {d.spec.engine === 'mysql' && (
            <p className="mt-3 text-sm text-muted-foreground">
              Application bindings mount the public CA. Configure your MySQL driver to use it and
              verify the endpoint hostname. The MySQL CLI requires --ssl-mode=VERIFY_IDENTITY and
              --ssl-ca pointing to that CA.
            </p>
          )}
          {d.spec.engine === 'mongodb' && (
            <p className="mt-3 text-sm text-muted-foreground">
              Bindings mount the public CA and configure TLS, hostname verification, replica-set
              discovery and majority reads and writes. The driver must be able to reach every
              advertised member. Standalone also uses a replica-set connection.
            </p>
          )}
          {d.spec.engine === 'clickhouse' && (
            <p className="mt-3 text-sm text-muted-foreground">
              Bindings use native ClickHouse TLS on port 9440. Configure the driver with the mounted
              public CA and endpoint hostname. A URL alone does not install private CA trust.
            </p>
          )}
          {d.spec.engine === 'oracle' && (
            <p className="mt-3 text-sm text-muted-foreground">
              Connect with TCPS on port 2484 to the FREEPDB1 service as APP. Configure your driver
              or client wallet with the public CA and verify the endpoint hostname. Never copy the
              server wallet or private key to an application.
            </p>
          )}
          {d.spec.engine === 'postgresql' && (
            <p className="mt-3 text-sm text-muted-foreground">
              Managed application bindings mount the public CA and use sslmode=verify-full. For
              external clients, set sslrootcert to the downloaded CA and connect using the endpoint
              hostname.
            </p>
          )}
          {error && (
            <p role="alert" className="mt-3 text-destructive">
              {error}
            </p>
          )}
        </div>
      )}
    </section>
  )
}
