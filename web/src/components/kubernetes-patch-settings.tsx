import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { client, unwrap } from '../lib/client'
import { APIError, message } from '../lib/api'
import { Button } from './ui/button'
import { Input } from './ui/input'
import { ErrorState, Loading, Note, RequestError } from './shared'
import { SettingsSection } from './settings-section'
import type { components } from '../lib/api.generated'

type Settings = components['schemas']['KubernetesPatchSettings']

const help =
  'Lets people with the kubernetes:patch permission add YAML patches to the Deployment, Service and Ingress Hakopod generates. Hakopod refuses patches that touch protected security fields. Turning this off blocks new or changed patches; services keep the patches they already have.'

export default function KubernetesPatchSettings() {
  const query = useQuery({
    queryKey: ['kubernetes-patch-settings'],
    queryFn: ({ signal }) => unwrap(client.GET('/settings/kubernetes-patches', { signal })),
    retry: false,
    refetchOnWindowFocus: false,
  })
  if (query.isPending || query.error || !query.data)
    return (
      <SettingsSection title="Kubernetes patches" help={help}>
        {query.isPending ? (
          <Loading />
        ) : (
          <ErrorState error={query.error} retry={() => void query.refetch()} />
        )}
      </SettingsSection>
    )
  return <Form initial={query.data} />
}

function Form({ initial }: { initial: Settings }) {
  const [enabled, setEnabled] = useState(initial.enabled)
  const [saved, setSaved] = useState<Settings>(initial)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [conflict, setConflict] = useState(false)
  const save = async () => {
    if (busy) return
    setBusy(true)
    setError('')
    setConflict(false)
    try {
      setSaved(
        await unwrap(
          client.PUT('/settings/kubernetes-patches', {
            body: { enabled, expected_revision: saved.revision },
          }),
        ),
      )
    } catch (e) {
      if (e instanceof APIError && e.status === 409) {
        try {
          setSaved(await unwrap(client.GET('/settings/kubernetes-patches')))
        } catch {}
        setConflict(true)
      } else setError(message(e))
    } finally {
      setBusy(false)
    }
  }
  return (
    <SettingsSection
      title="Kubernetes patches"
      help={help}
      state={saved.enabled ? 'On · patches need kubernetes:patch' : 'Off'}
    >
      <form
        onSubmit={(e) => {
          e.preventDefault()
          void save()
        }}
      >
        <label className="checkbox-row">
          <Input type="checkbox" checked={enabled} onChange={(e) => setEnabled(e.target.checked)} />
          Allow raw Kubernetes patches on services
        </label>
        {conflict && (
          <Note>
            Someone else changed this setting. It is currently {saved.enabled ? 'on' : 'off'};
            review and save again.
          </Note>
        )}
        {error && <RequestError error={error} />}
        <div className="settings-inline-controls">
          <Button type="submit" variant="primary" disabled={busy || enabled === saved.enabled}>
            {busy ? 'Saving…' : 'Save'}
          </Button>
        </div>
      </form>
    </SettingsSection>
  )
}
