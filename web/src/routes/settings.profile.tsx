import { Input } from '../components/ui/input'
import { useState } from 'react'
import { createFileRoute, Link } from '@tanstack/react-router'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { client, unwrap } from '../lib/client'
import { message } from '../lib/api'
import type { components } from '../lib/api.generated'
import { FormPage, FormSection, FormHint } from '../components/form-page'
import { Avatar, avatarURL } from '../components/avatar'
import { Button } from '../components/ui/button'
import { Icon } from '../components/icons'
import { ErrorState, Loading, Note } from '../components/shared'
export const Route = createFileRoute('/settings/profile')({ component: ProfilePage })
function ProfilePage() {
  const profile = useQuery({
    queryKey: ['profile'],
    queryFn: ({ signal }) => unwrap(client.GET('/auth/profile', { signal })),
    gcTime: 0,
  })
  if (profile.isPending) return <Loading />
  if (profile.error || !profile.data) return <ErrorState error={profile.error} />
  return <ProfileForm profile={profile.data} />
}
function ProfileForm({ profile }: { profile: components['schemas']['Profile'] }) {
  const cache = useQueryClient()
  const [name, setName] = useState(profile.name)
  const [style, setStyle] = useState(profile.avatar_style)
  const [seed, setSeed] = useState(profile.avatar_seed || crypto.randomUUID())
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [saved, setSaved] = useState(false)
  return (
    <FormPage
      title="Your profile"
      description="Choose how your name and avatar appear across the workspace."
      breadcrumbs={[{ label: 'Account & access', to: '/settings' }, { label: 'Profile' }]}
      icon="user"
      help={
        <>
          <FormHint title="A consistent identity">
            Your profile is shared across teams. Each team can give you its own unique username.
          </FormHint>
          <FormHint title="Avatar privacy">
            Generated styles use an opaque random seed. Your email address is never sent to
            DiceBear.
          </FormHint>
        </>
      }
    >
      <form
        onSubmit={async (event) => {
          event.preventDefault()
          if (busy) return
          setBusy(true)
          setError('')
          setSaved(false)
          try {
            const result = await unwrap(
              client.PATCH('/auth/profile', {
                body: {
                  name,
                  avatar_style: style,
                  avatar_seed: seed,
                  expected_revision: profile.revision,
                },
              }),
            )
            cache.setQueryData(['profile'], result)
            void cache.invalidateQueries({ queryKey: ['me'] })
            setSaved(true)
          } catch (err) {
            setError(message(err))
          } finally {
            setBusy(false)
          }
        }}
      >
        <div className="form-body">
          <FormSection title="Display name" icon="user">
            <div className="profile-preview">
              <Avatar name={name} url={avatarURL(style, seed)} size={72} />
              <div>
                <strong>{name || 'Your name'}</strong>
                <p>
                  {style === 'initials'
                    ? 'Initials'
                    : style === 'glass'
                      ? 'Gradient glass'
                      : 'Identicon'}
                </p>
              </div>
            </div>
            <label>
              Name
              <Input
                required
                maxLength={100}
                value={name}
                onChange={(event) => setName(event.target.value)}
                autoComplete="name"
              />
            </label>
          </FormSection>
          <FormSection
            title="Avatar style"
            description="Select a style, then shuffle until it feels like you."
            icon="grid"
          >
            <div className="avatar-options">
              {(['initials', 'identicon', 'glass'] as const).map((value) => (
                <button
                  type="button"
                  key={value}
                  className={style === value ? 'selected' : ''}
                  aria-pressed={style === value}
                  onClick={() => setStyle(value)}
                >
                  <Avatar name={name} url={avatarURL(value, seed)} size={64} />
                  <strong>
                    {value === 'initials'
                      ? 'Initials'
                      : value === 'glass'
                        ? 'Gradient glass'
                        : 'Identicon'}
                  </strong>
                </button>
              ))}
            </div>
            <Button
              type="button"
              disabled={style === 'initials'}
              onClick={() => setSeed(crypto.randomUUID())}
            >
              <Icon name="refresh" size={14} />
              Shuffle avatar
            </Button>
          </FormSection>
          {error && <ErrorState error={error} />} {saved && <Note>Profile saved.</Note>}
        </div>
        <div className="form-footer">
          <Link className="button" to="/settings">
            Back to account
          </Link>
          <Button type="submit" variant="primary" disabled={busy}>
            {busy ? 'Saving…' : 'Save profile'}
          </Button>
        </div>
      </form>
    </FormPage>
  )
}
