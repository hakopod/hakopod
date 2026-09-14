import { useEffect, useState } from 'react'
import { Link } from '@tanstack/react-router'
import { Icon } from './icons'
import { Tooltip } from './ui/surfaces'
import { useQuery } from '@tanstack/react-query'
import { useScope } from '../lib/scope'
import { useAuthStatus } from '../lib/installation-settings'
import { client, unwrap } from '../lib/client'
import { message, timestamp } from '../lib/api'
import { Button } from './ui/button'
import { Input } from './ui/input'
import { Dialog } from './ui/dialog'
import { ErrorState, Loading, Note, Status } from './shared'

export function useInstallationOwner() {
  const scope = useScope()
  const auth = useAuthStatus()
  return (
    scope.identity.owner && scope.identity.admin && auth.data?.deployment_mode === 'self-hosted'
  )
}
function useVisible() {
  const [visible, setVisible] = useState(false)
  useEffect(() => {
    const update = () => setVisible(document.visibilityState === 'visible')
    update()
    document.addEventListener('visibilitychange', update)
    return () => document.removeEventListener('visibilitychange', update)
  }, [])
  return visible
}
export function InstallationLogs() {
  const allowed = useInstallationOwner()
  const visible = useVisible()
  const [paused, setPaused] = useState(false)
  const logs = useQuery({
    queryKey: ['installation-logs'],
    queryFn: ({ signal }) => unwrap(client.GET('/installation/logs', { signal })),
    enabled: allowed && visible && !paused,
    refetchInterval: visible && !paused ? 5000 : false,
    refetchOnWindowFocus: false,
    refetchOnReconnect: false,
    retry: false,
  })
  if (!allowed) return <Note>API logs are available to the self-hosted installation owner.</Note>
  return (
    <section className="flex flex-col gap-4">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <h2>API server logs</h2>
        <div className="flex items-center gap-2">
          <Button onClick={() => setPaused(!paused)}>{paused ? 'Resume' : 'Pause'}</Button>
          <Button disabled={logs.isFetching} onClick={() => void logs.refetch()}>
            Refresh
          </Button>
        </div>
      </div>
      <p className="text-sm text-muted-foreground">
        Last 200 journal entries.{' '}
        {paused ? 'Automatic refresh paused.' : 'Checks every 5 seconds while visible.'}{' '}
        {logs.data && `Updated ${timestamp(logs.data.observed_at)}.`}
      </p>
      {logs.error && <ErrorState error={logs.error} />}
      {logs.isPending ? (
        <Loading />
      ) : (
        logs.data && (
          <pre
            className="code-panel max-h-[32rem] overflow-auto whitespace-pre-wrap break-words p-4 text-xs"
            tabIndex={0}
            aria-label="API server log output"
          >
            {logs.data.entries.length
              ? logs.data.entries
                  .map(
                    (entry) =>
                      `${new Date(Number(entry.timestamp) / 1000).toISOString()} ${entry.message}`,
                  )
                  .join('\n')
              : 'No journal entries available.'}
          </pre>
        )
      )}
    </section>
  )
}
export function InstallationSetup() {
  const allowed = useInstallationOwner()
  const setup = useQuery({
    queryKey: ['installation-setup'],
    queryFn: ({ signal }) => unwrap(client.GET('/installation/setup', { signal })),
    enabled: allowed,
    retry: false,
  })
  if (!allowed)
    return (
      <Note>
        Ask the self-hosted installation owner to configure storage and the readiness helper.
      </Note>
    )
  if (setup.isPending) return <Loading />
  if (setup.error || !setup.data)
    return <ErrorState error={setup.error} retry={() => void setup.refetch()} />
  return (
    <div className="flex flex-col gap-6">
      <section className="flex flex-col gap-3">
        <h2>Listener readiness</h2>
        <p>
          SMTP and combined checks need the Hakopod probe helper. Releases include a prebuilt image
          for Linux amd64 and arm64; no local build is needed.
        </p>
        <p>
          Open your installed version’s{' '}
          <a
            className="underline! underline-offset-4"
            href="https://github.com/hakopod/hakopod/releases"
            target="_blank"
            rel="noreferrer"
          >
            release assets
          </a>{' '}
          and copy the complete image reference from <code>probe-image.txt</code>. Releases before
          this asset was introduced require a local build; see the setup guide below.
        </p>
        <p>
          Current helper:{' '}
          <code className="break-all">{setup.data?.readiness_image || 'Not configured'}</code>
        </p>
        <p>
          Add the setting below with <code>sudo systemctl edit hakopod-api</code>, replace the
          example with the release’s digest-pinned image reference, then restart the API with{' '}
          <code>sudo systemctl restart hakopod-api</code>.
        </p>
        <pre
          className="code-panel overflow-auto! p-4"
          tabIndex={0}
          aria-label="Readiness helper systemd configuration"
        >
          {
            '[Service]\nEnvironment="HAKOPOD_READINESS_PROBE_IMAGE=ghcr.io/hakopod/hakopod-probe@sha256:RELEASE_DIGEST"'
          }
        </pre>
        <p>
          Operator TOML also supports <code>server.readiness_probe_image</code>. Set{' '}
          <code>HAKOPOD_CONFIG_FILE</code> to its path. Review the deployment again after
          configuration, then redeploy affected services to use the new helper.
        </p>
        <a
          href="https://github.com/hakopod/hakopod/blob/main/docs/readiness.md"
          target="_blank"
          rel="noreferrer"
        >
          Readiness setup guide
        </a>
      </section>
      <section className="flex flex-col gap-3">
        <h2>Persistent storage</h2>
        <p>
          Default storage class:{' '}
          <code>{setup.data?.default_storage_class || 'None configured'}</code>.
        </p>
        <p>
          Database templates need a storage class. Enable the installer’s optional local-volume
          module for a single node, or configure a CSI storage provider. Local volumes stay on that
          node and need separate backups.
        </p>
        <a
          href="https://github.com/hakopod/hakopod/blob/main/docs/installation-maintenance.md#optional-modules"
          target="_blank"
          rel="noreferrer"
        >
          Configure storage and certificates
        </a>
      </section>
      <section className="flex flex-col gap-3">
        <h2>Maintenance service</h2>
        <p>
          Logs and upgrades use the installer’s local maintenance service. On supported releases,
          check it with <code>sudo systemctl status hakopod-maintenance</code>. Older installs need
          the maintenance bootstrap described in the upgrade guide.
        </p>
        <a
          href="https://github.com/hakopod/hakopod/blob/main/docs/installation-maintenance.md"
          target="_blank"
          rel="noreferrer"
        >
          Installation maintenance guide
        </a>
      </section>
    </div>
  )
}
export function InstallationUpdates() {
  const allowed = useInstallationOwner()
  const visible = useVisible()
  const status = useQuery({
    queryKey: ['installation-status'],
    queryFn: ({ signal }) => unwrap(client.GET('/installation/status', { signal })),
    enabled: allowed && visible,
    refetchInterval: visible ? 10000 : false,
    retry: false,
  })
  const [review, setReview] = useState('')
  const [confirmation, setConfirmation] = useState('')
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState('')
  const [submitted, setSubmitted] = useState(false)
  const operation = status.data?.operation
  const running = operation && !['idle', 'succeeded', 'failed'].includes(operation.status)
  useEffect(() => {
    if (running || operation?.status === 'succeeded' || operation?.status === 'failed')
      setSubmitted(false)
  }, [running, operation?.status])
  if (!allowed) return <Note>Upgrades are managed by the self-hosted installation owner.</Note>
  async function upgrade() {
    setSaving(true)
    setError('')
    try {
      await unwrap(
        client.POST('/installation/upgrade', { body: { version: review, confirmation } }),
      )
      setSubmitted(true)
      setReview('')
      setConfirmation('')
      void status.refetch()
    } catch (e) {
      setError(message(e))
    } finally {
      setSaving(false)
    }
  }
  return (
    <section className="flex flex-col gap-4">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <h2>Hakopod updates</h2>
        <Button disabled={status.isFetching} onClick={() => void status.refetch()}>
          Refresh status
        </Button>
      </div>
      {status.error && <ErrorState error={status.error} />}
      {status.isPending && <Loading />}
      {status.data && (
        <>
          <dl className="grid grid-cols-[auto_1fr] gap-x-6 gap-y-2 text-sm">
            <dt>Installed</dt>
            <dd>{status.data.current_version}</dd>
            <dt>Latest compatible release</dt>
            <dd>{status.data.latest_version || 'Unavailable'}</dd>
            <dt>Release check</dt>
            <dd>{status.data.checked_at ? timestamp(status.data.checked_at) : 'Unavailable'}</dd>
          </dl>
          {status.data.check_error && <Note>{status.data.check_error}</Note>}
          {operation && operation.status !== 'idle' && (
            <div className="flex flex-wrap items-center gap-3">
              <Status value={operation.status} />
              <p>{operation.message}</p>
            </div>
          )}
          {status.data.update_available && (
            <Button
              variant="primary"
              disabled={!!running || saving || submitted}
              onClick={() => {
                setReview(status.data!.latest_version)
                setConfirmation('')
                setError('')
              }}
            >
              Review upgrade
            </Button>
          )}
          {!status.data.update_available && !status.data.check_error && (
            <p>No newer release with an upgrade package is available for this release series.</p>
          )}
        </>
      )}
      {submitted && <Note>Upgrade requested. Status will reconnect after the API restarts.</Note>}
      <p className="text-sm text-muted-foreground">
        Release checks are cached for 15 minutes. Alpha installations include prereleases; stable
        installations stay on stable releases.
      </p>
      <Dialog
        title={`Upgrade to ${review}`}
        description="Review the management-service restart and backups before continuing."
        open={!!review}
        onOpenChange={(open) => {
          if (!saving && !open) setReview('')
        }}
      >
        <div className="dialog-body flex flex-col gap-4">
          <p>
            The API and dashboard stop briefly while PostgreSQL and configuration are backed up.
            Your applications and K3s keep running.
          </p>
          <p>
            If new migrations fail, an administrator must recover using the retained backups. The
            updater will not roll the database back automatically.
          </p>
          <label htmlFor="upgrade-confirmation">
            Type <code>upgrade {review}</code> to continue.
          </label>
          <Input
            id="upgrade-confirmation"
            value={confirmation}
            onChange={(e) => setConfirmation(e.target.value)}
            autoComplete="off"
          />
          {error && <ErrorState error={error} />}
        </div>
        <div className="dialog-footer">
          <Button disabled={saving} onClick={() => setReview('')}>
            Cancel
          </Button>
          <Button
            variant="primary"
            disabled={saving || confirmation !== `upgrade ${review}`}
            onClick={() => void upgrade()}
          >
            {saving ? 'Requesting…' : 'Upgrade Hakopod'}
          </Button>
        </div>
      </Dialog>
    </section>
  )
}

export function InstallationUpdateNotice() {
  const allowed = useInstallationOwner()
  const visible = useVisible()
  const status = useQuery({
    queryKey: ['installation-status'],
    queryFn: ({ signal }) => unwrap(client.GET('/installation/status', { signal })),
    enabled: allowed && visible,
    staleTime: 15 * 60 * 1000,
    refetchInterval: visible ? 15 * 60 * 1000 : false,
    retry: false,
  })
  if (!allowed || !status.data?.update_available) return null
  return (
    <Tooltip content={`Hakopod ${status.data.latest_version} is available`}>
      <Button asChild variant="ghost" size="icon">
        <Link
          to="/infrastructure"
          search={{ tab: 'updates' }}
          aria-label={`Upgrade available: Hakopod ${status.data.latest_version}`}
        >
          <Icon name="refresh" />
        </Link>
      </Button>
    </Tooltip>
  )
}
