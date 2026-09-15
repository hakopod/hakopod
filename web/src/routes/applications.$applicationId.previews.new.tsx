import { useRef, useState } from 'react'
import { createFileRoute, useNavigate } from '@tanstack/react-router'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import type { Application, Spec } from '../lib/types'
import { client, unwrap } from '../lib/client'
import { APIError, message } from '../lib/api'
import { useResourceScope, useScope } from '../lib/scope'
import { specToTOML } from '../lib/toml'
import { FormPage, FormSection } from '../components/form-page'
import { Button } from '../components/ui/button'
import { Input } from '../components/ui/input'
import { Textarea } from '../components/ui/textarea'
import { SelectField } from '../components/ui/select'
import { Empty, ErrorState, Loading } from '../components/shared'

export const Route = createFileRoute('/applications/$applicationId/previews/new')({
  component: NewPreview,
})
function NewPreview() {
  const { applicationId } = Route.useParams()
  const scope = useScope()
  const query = useQuery({
    queryKey: ['application', applicationId],
    queryFn: ({ signal }) =>
      unwrap(client.GET('/applications/{id}', { signal, params: { path: { id: applicationId } } })),
    gcTime: 0,
  })
  useResourceScope(query.data)
  if (query.isPending) return <Loading />
  if (query.error || !query.data) return <ErrorState error={query.error} />
  if (
    !scope.identity.can_manage_previews &&
    !scope.identity.admin &&
    !scope.identity.project_roles?.some(
      (role) => role.project === query.data.project && role.role === 'admin',
    )
  )
    return (
      <Empty
        title="Project administrator access required"
        description="A project administrator can create temporary preview environments."
      />
    )
  return <PreviewForm application={query.data} />
}
export function PreviewForm({ application }: { application: Application }) {
  const [snapshot, setSnapshot] = useState(application)
  const [name, setName] = useState(''),
    [branch, setBranch] = useState(''),
    [hours, setHours] = useState('24'),
    [discard, setDiscard] = useState(false)
  const [toml, setToml] = useState(() => {
    const first = Object.entries(snapshot.spec.services).find(([, s]) => !s.job)
    const spec: Spec = { schema_version: 1, name: 'preview', services: {} }
    if (first) {
      const [name, s] = first
      spec.services[name] = {
        image: s.image,
        port: s.port,
        public: s.public,
        size: 'small',
        replicas: 1,
        registry_credential: s.registry_credential,
      }
    }
    return specToTOML(spec)
  })
  const submitting = useRef(false)
  const [conflict, setConflict] = useState(false)
  const [busy, setBusy] = useState(false),
    [error, setError] = useState(''),
    [review, setReview] = useState(false)
  const requestKey = useRef(crypto.randomUUID()),
    requestBody = useRef<string>('')
  const navigate = useNavigate(),
    cache = useQueryClient()
  return (
    <FormPage
      title="Create preview"
      breadcrumbs={[
        { label: snapshot.name, to: `/applications/${snapshot.id}?tab=previews` },
        { label: 'Create preview' },
      ]}
      description="Review an isolated, temporary deployment."
    >
      <form
        onSubmit={async (e) => {
          e.preventDefault()
          if (submitting.current || conflict) return
          if (!review) {
            setReview(true)
            return
          }
          submitting.current = true
          setBusy(true)
          setError('')
          const body = {
            name,
            branch,
            ttl_hours: Number(hours),
            expected_parent_revision: snapshot.revision,
            toml,
            discard_on_expiry: discard,
          }
          const serialized = JSON.stringify(body)
          if (requestBody.current && requestBody.current !== serialized)
            requestKey.current = crypto.randomUUID()
          requestBody.current = serialized
          try {
            const result = await unwrap(
              client.POST('/applications/{id}/previews', {
                params: {
                  path: { id: snapshot.id },
                  header: { 'Idempotency-Key': requestKey.current },
                },
                body,
              }),
            )
            void cache.invalidateQueries({ queryKey: ['previews', snapshot.id] })
            void navigate({
              to: '/applications/$applicationId',
              params: { applicationId: result.deployment.application_id },
            })
          } catch (err) {
            setError(message(err))
            setConflict(err instanceof APIError && err.status === 409)
          } finally {
            submitting.current = false
            setBusy(false)
          }
        }}
      >
        <div className="form-body grid gap-4">
          <FormSection title="Preview">
            <div className="grid gap-4 sm:grid-cols-2">
              <label>
                Name
                <Input
                  required
                  pattern="[a-z][a-z0-9-]{0,31}"
                  maxLength={32}
                  value={name}
                  disabled={busy || review}
                  onChange={(e) => setName(e.target.value)}
                  placeholder="pr-123"
                />
              </label>
              <label>
                Branch or pull request
                <Input
                  value={branch}
                  disabled={busy || review}
                  maxLength={200}
                  onChange={(e) => setBranch(e.target.value)}
                />
              </label>
            </div>
            <p className="field-help">
              This reference labels the preview. Build your branch in GitHub or GitLab first, then
              use its image below.
            </p>
            <label>
              Lifetime
              <SelectField
                label="Lifetime"
                value={hours}
                disabled={busy || review}
                onValueChange={setHours}
                options={[1, 6, 24, 48, 72].map((value) => ({
                  value: String(value),
                  label: `${value} hours`,
                }))}
              />
            </label>
          </FormSection>
          <FormSection title="Workloads">
            <p id="preview-toml-help" className="field-help">
              The starter includes one service image. Add required services and configuration
              explicitly. Production variables, secret values, shared networks and data are not
              copied. Each preview supports four small services, one replica each and up to 5 GiB of
              fresh storage.
            </p>
            <label>
              Preview TOML
              <Textarea
                className="code-editor min-h-64"
                aria-describedby="preview-toml-help"
                required
                spellCheck={false}
                maxLength={262144}
                value={toml}
                readOnly={busy || review}
                onChange={(e) => setToml(e.target.value)}
              />
            </label>
          </FormSection>
          <label className="grid grid-cols-[auto_1fr] items-start gap-x-2 [&>.field-error]:col-span-2 [&>.field-error]:row-start-2">
            <Input
              type="checkbox"
              checked={discard}
              required
              disabled={busy || review}
              onChange={(e) => setDiscard(e.target.checked)}
            />
            <span className="col-start-2 row-start-1">
              Start deleting this preview's workloads, volumes and native secrets when its lifetime
              ends.
            </span>
          </label>
          {review && (
            <p role="status">
              Ready to create {name} for {hours} hours from parent revision {snapshot.revision}.
              Review the TOML above before deploying.
            </p>
          )}
          {conflict && (
            <Button
              type="button"
              disabled={busy}
              onClick={async () => {
                setBusy(true)
                try {
                  const latest = await unwrap(
                    client.GET('/applications/{id}', { params: { path: { id: snapshot.id } } }),
                  )
                  setSnapshot(latest)
                  setReview(false)
                  setConflict(false)
                  setError('')
                } catch (err) {
                  setError(message(err))
                } finally {
                  setBusy(false)
                }
              }}
            >
              Refresh parent revision and review draft
            </Button>
          )}
          {error && (
            <p role="alert" className="inline-error">
              {error}
            </p>
          )}
        </div>
        <div className="form-footer">
          <Button
            type="button"
            disabled={busy}
            onClick={() =>
              review
                ? setReview(false)
                : void navigate({
                    to: '/applications/$applicationId',
                    params: { applicationId: snapshot.id },
                    search: { tab: 'previews' },
                  })
            }
          >
            {review ? 'Edit settings' : 'Cancel'}
          </Button>
          <Button variant="primary" disabled={busy || !discard || conflict} type="submit">
            {busy ? 'Creating…' : review ? 'Create preview' : 'Review preview'}
          </Button>
        </div>
      </form>
    </FormPage>
  )
}
