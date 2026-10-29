// Development-only design study for the open-source authentication screens.
// It renders the dashboard's own form components inside the proposed layout and
// sends no requests. Serve with the review Vite config and open:
// /scripts/auth-design-review.html#login
import '../src/styles.css'
import '../src/styles/account-access.css'
import './auth-design-review.css'
import { useEffect, useRef, useState, type FormEvent, type ReactNode } from 'react'
import { createRoot } from 'react-dom/client'
import { CheckField } from '@hakopod/hatch-ui/components/checkbox'
import { InputOTP } from '@hakopod/hatch-ui/components/input-otp'
import { Button } from '../src/components/ui/button'
import { Field } from '../src/components/ui/field'
import { PasswordField } from '../src/components/ui/password-field'
import { ServiceIcon } from '../src/components/service-icon'
import { Logo } from '../src/components/icons'

type View = 'login' | 'setup' | 'signup' | 'forgot' | 'reset' | 'verify' | 'mfa' | 'invite'
type Texture = 'mosaic' | 'field'

const views: Record<View, string> = {
  login: 'Sign in',
  setup: 'First-run setup',
  signup: 'Create account',
  forgot: 'Forgot password',
  reset: 'Reset password',
  verify: 'Verify email',
  mfa: 'Two-factor',
  invite: 'Invite',
}

const headlines: Record<View, string> = {
  login: 'Your servers, your rules — ship anything you can build.',
  setup: 'A fresh installation. Make it yours.',
  signup: 'Bring your team. Keep your infrastructure.',
  forgot: "Locked out happens. Let's get you back in.",
  reset: 'A new key for the same door.',
  verify: 'One last step before the fun part.',
  mfa: 'Two keys are better than one.',
  invite: 'Someone wants you on the team.',
}

// What a successful submit leads to, so the flow can be walked end to end.
const next: Partial<Record<View, View>> = {
  login: 'mfa',
  signup: 'verify',
  forgot: 'verify',
  reset: 'login',
  setup: 'login',
  invite: 'login',
  mfa: 'login',
}

function initialView(): View {
  const hash = location.hash.slice(1)
  return hash in views ? (hash as View) : 'login'
}

function Providers() {
  const [opening, setOpening] = useState('')
  const providers = [
    ['google', 'Google', '/icons/google-color.svg'],
    ['github', 'GitHub', ''],
    ['gitlab', 'GitLab', '/icons/gitlab-color.svg'],
  ] as const
  return (
    <>
      <div className="auth-study-providers" role="group" aria-label="Continue with an identity provider">
        {providers.map(([id, label, src]) => (
          <Button
            key={id}
            type="button"
            variant="outline"
            aria-label={`Continue with ${label}`}
            aria-busy={opening === id || undefined}
            onClick={() => {
              setOpening(id)
              setTimeout(() => setOpening(''), 1200)
            }}
          >
            {src ? <img className="auth-provider-logo" src={src} alt="" width={20} height={20} /> : <ServiceIcon name={id} size={20} />}
            {opening === id ? 'Opening…' : label}
          </Button>
        ))}
      </div>
      <div className="hako-auth-divider">
        <span>Or use your email</span>
      </div>
    </>
  )
}

function Submit({ children, busy }: { children: ReactNode; busy: boolean }) {
  return (
    <Button type="submit" className="auth-study-submit" aria-busy={busy || undefined} disabled={busy}>
      {busy ? 'Working…' : children}
      <span aria-hidden="true">→</span>
    </Button>
  )
}

function Screen({ view, go }: { view: View; go: (view: View) => void }) {
  const [busy, setBusy] = useState(false)
  const [agree, setAgree] = useState(false)
  const [code, setCode] = useState('')
  const [password, setPassword] = useState('')

  function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    if (!event.currentTarget.reportValidity()) return
    setBusy(true)
    setTimeout(() => {
      setBusy(false)
      const to = next[view]
      if (to) go(to)
    }, 700)
  }

  const link = (to: View, label: string) => (
    <Button type="button" variant="ghost" size="sm" className="auth-study-link" onClick={() => go(to)}>
      {label}
    </Button>
  )
  const name = <Field label="Your name" autoComplete="name" required />
  const email = <Field label="Email address" type="email" autoComplete="email" required placeholder="you@company.com" />
  const newPassword = (
    <PasswordField
      label="Password"
      autoComplete="new-password"
      minLength={12}
      required
      placeholder="At least 12 characters"
      value={password}
      onChange={(event) => setPassword(event.target.value)}
    />
  )

  const body: Record<View, { lede: ReactNode; fields?: ReactNode; aside?: ReactNode; actions: ReactNode; form?: boolean }> = {
    login: {
      lede: 'Sign in to deploy and operate your applications.',
      fields: (
        <>
          <Providers />
          {email}
          <PasswordField label="Password" autoComplete="current-password" required />
        </>
      ),
      actions: <>{link('forgot', 'Forgot password?')}<Submit busy={busy}>Sign in</Submit></>,
    },
    setup: {
      lede: 'Create the first owner account for this Hakopod installation.',
      fields: (
        <>
          {name}
          {email}
          {newPassword}
          <Field
            label="Installer credential"
            className="auth-study-mono"
            required
            autoComplete="off"
            spellCheck={false}
            placeholder="Setup token or admin key"
            help="Printed by the installer, or in /etc/hakopod/bootstrap."
          />
        </>
      ),
      actions: <><span /><Submit busy={busy}>Create owner</Submit></>,
    },
    signup: {
      lede: 'Create a personal workspace or join a team that invited you.',
      fields: (
        <>
          <Providers />
          {name}
          {email}
          {newPassword}
          <CheckField label="I agree to this installation's acceptable use policy" checked={agree} onCheckedChange={setAgree} />
        </>
      ),
      actions: <>{link('login', 'I have an account')}<Submit busy={busy}>Create account</Submit></>,
    },
    forgot: {
      lede: "Enter your email and we'll send a link to choose a new password.",
      fields: email,
      aside: (
        <p className="auth-study-note">
          <b>No email set up?</b> An owner can reset your password from Settings → Members, or run{' '}
          <code>hakopod admin reset-password</code> on the server.
        </p>
      ),
      actions: <>{link('login', 'Back to sign in')}<Submit busy={busy}>Send reset link</Submit></>,
    },
    reset: {
      lede: 'Signed-in sessions on other devices will be signed out.',
      fields: (
        <>
          {newPassword}
          <PasswordField label="Confirm password" autoComplete="new-password" required pattern={password.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')} />
        </>
      ),
      actions: <><span /><Submit busy={busy}>Save password</Submit></>,
    },
    verify: {
      form: false,
      lede: 'We sent a verification link. Open it on this device to finish signing up.',
      aside: (
        <p className="auth-study-mail">
          ada@example.com<small>The link expires in 30 minutes.</small>
        </p>
      ),
      actions: (
        <>
          {link('signup', 'Use a different email')}
          <Button type="button" className="auth-study-submit">Resend link<span aria-hidden="true">↻</span></Button>
        </>
      ),
    },
    mfa: {
      lede: 'Enter the 6-digit code from your authenticator app.',
      fields: <InputOTP value={code} onChange={setCode} label="Authenticator code" />,
      actions: (
        <>
          <Button type="button" variant="ghost" size="sm" className="auth-study-link">Use a recovery code</Button>
          <Submit busy={busy}>Verify</Submit>
        </>
      ),
    },
    invite: {
      lede: (
        <>
          Grace Hopper invited you to the <b>Studio</b> team as a developer.
        </>
      ),
      fields: (
        <>
          <Providers />
          {name}
          <Field label="Email address" type="email" value="ada@example.com" readOnly />
          {newPassword}
        </>
      ),
      actions: (
        <>
          <Button type="button" variant="ghost" size="sm" className="auth-study-link">Decline invite</Button>
          <Submit busy={busy}>Accept and join</Submit>
        </>
      ),
    },
  }
  const current = body[view]
  const title = { login: 'Sign in', setup: 'Set up Hakopod', signup: 'Create account', forgot: 'Reset password', reset: 'Choose a new password', verify: 'Check your inbox', mfa: 'Two-factor check', invite: 'Join Studio' }[view]
  const content = (
    <>
      <h1>{title}</h1>
      <p className="auth-study-lede">{current.lede}</p>
      {current.fields && <div className="auth-study-fields">{current.fields}</div>}
      {current.aside}
      <div className="auth-study-actions">{current.actions}</div>
    </>
  )
  return current.form === false ? (
    <div className="auth-study-view">{content}</div>
  ) : (
    <form className="auth-study-view" onSubmit={submit} key={view}>
      {content}
    </form>
  )
}

// Layered sines: smooth, deterministic and cheap, so no noise library is needed.
const wave = (x: number, y: number, t: number) =>
  (Math.sin(x * 1.7 + t * 0.7) + Math.sin(y * 2.3 - t * 0.5 + x * 0.6) + Math.sin((x + y) * 1.1 + t * 0.3) + Math.sin(x * 3.9 - y * 1.3 + t)) / 4
const ramp = [[12, 28, 22], [22, 52, 40], [47, 86, 65], [96, 170, 128], [159, 232, 191], [216, 255, 69]]
function shade(value: number) {
  const t = Math.max(0, Math.min(0.999, value)) * (ramp.length - 1)
  const i = Math.floor(t)
  return ramp[i].map((v, k) => Math.round(v + (ramp[i + 1][k] - v) * (t - i)))
}
const hash = (a: number, b: number) => {
  const s = Math.sin(a * 127.1 + b * 311.7) * 43758.5453
  return s - Math.floor(s)
}

function mosaic(ctx: CanvasRenderingContext2D, w: number, h: number, t: number) {
  ctx.fillStyle = '#0c1c16'
  ctx.fillRect(0, 0, w, h)
  // Staggered vertical bars echo the blocks of the Hakopod mark.
  const col = Math.max(18, Math.round(w / 34))
  for (let x = 0, c = 0; x < w; x += col, c++) {
    for (let y = -hash(c, 99) * col * 3, r = 0; y < h; r++) {
      const seg = col * (0.8 + hash(c, r) * 3.6)
      const glow = wave(c * 0.18, (y / h) * 3, t) * 0.5 + 0.5
      const [R, G, B] = shade(Math.pow(glow, 2.2) * (0.35 + 0.65 * Math.sin((y / h) * Math.PI)) * (0.55 + (0.45 * x) / w) * 1.15)
      const g = ctx.createLinearGradient(x, y, x, y + seg)
      g.addColorStop(0, `rgba(${R},${G},${B},.55)`)
      g.addColorStop(0.5, `rgb(${R},${G},${B})`)
      g.addColorStop(1, `rgba(${R},${G},${B},.7)`)
      ctx.fillStyle = g
      ctx.fillRect(x, y, col - 1, seg - 1)
      y += seg
    }
  }
  const fade = ctx.createLinearGradient(0, h * 0.45, 0, h)
  fade.addColorStop(0, 'rgba(12,28,22,0)')
  fade.addColorStop(1, 'rgba(12,28,22,.92)')
  ctx.fillStyle = fade
  ctx.fillRect(0, 0, w, h)
}

function field(ctx: CanvasRenderingContext2D, w: number, h: number) {
  // Warped concentric ridges at low resolution, scaled up for a soft woven surface.
  const s = 3
  const cw = Math.ceil(w / s)
  const ch = Math.ceil(h / s)
  const image = ctx.createImageData(cw, ch)
  const cx = cw * 0.58
  const cy = ch * 0.52
  for (let y = 0; y < ch; y++)
    for (let x = 0; x < cw; x++) {
      const dx = (x - cx) / cw
      const dy = (y - cy) / cw
      const dist = Math.sqrt(dx * dx * 1.2 + dy * dy) + wave(dx * 3, dy * 3, 0.5) * 0.35
      const ridge = Math.pow(Math.sin(dist * 26) * 0.5 + 0.5, 2.4)
      const v = Math.max(0, ridge * (0.25 + 0.75 * Math.max(0, 1 - dist * 1.25)) * 0.62 + (Math.random() - 0.5) * 0.18 * ridge)
      const i = (y * cw + x) * 4
      image.data.set([10 + v * 120, 22 + v * 170, 10 + v * 40, 255], i)
    }
  const off = document.createElement('canvas')
  off.width = cw
  off.height = ch
  off.getContext('2d')!.putImageData(image, 0, 0)
  ctx.drawImage(off, 0, 0, w, h)
  const fade = ctx.createLinearGradient(0, h * 0.5, 0, h)
  fade.addColorStop(0, 'rgba(8,18,10,0)')
  fade.addColorStop(1, 'rgba(8,18,10,.85)')
  ctx.fillStyle = fade
  ctx.fillRect(0, 0, w, h)
}

function Art({ texture }: { texture: Texture }) {
  const ref = useRef<HTMLCanvasElement>(null)
  useEffect(() => {
    const canvas = ref.current!
    const ctx = canvas.getContext('2d')!
    const still = matchMedia('(prefers-reduced-motion: reduce)').matches
    let frame = 0
    let last = 0
    let w = 0
    let h = 0
    const size = () => {
      const rect = canvas.getBoundingClientRect()
      const dpr = Math.min(devicePixelRatio || 1, 2)
      w = Math.round(rect.width)
      h = Math.round(rect.height)
      canvas.width = w * dpr
      canvas.height = h * dpr
      ctx.setTransform(dpr, 0, 0, dpr, 0, 0)
      if (texture === 'field') field(ctx, w, h)
      else if (still) mosaic(ctx, w, h, 1.3)
    }
    const tick = (ms: number) => {
      // About 12 frames a second keeps the motion slow and cheap.
      if (ms - last > 80) {
        last = ms
        mosaic(ctx, w, h, ms / 4000)
      }
      frame = requestAnimationFrame(tick)
    }
    size()
    if (texture === 'mosaic' && !still) frame = requestAnimationFrame(tick)
    addEventListener('resize', size)
    return () => {
      cancelAnimationFrame(frame)
      removeEventListener('resize', size)
    }
  }, [texture])
  return <canvas ref={ref} aria-hidden="true" />
}

function Study() {
  const [view, setView] = useState<View>(initialView)
  const [texture, setTexture] = useState<Texture>('mosaic')
  const go = (to: View) => {
    setView(to)
    history.replaceState(null, '', `#${to}`)
  }
  return (
    <>
      <main className="auth-study">
        <section className="auth-study-art" aria-label="Hakopod">
          <Art texture={texture} />
          <div className="auth-study-art-inner">
            <div className="auth-study-art-top">
              <span className="auth-study-brand">
                <Logo size={30} />
                hakopod
              </span>
              <span className="auth-study-pill">
                <i />
                Open source · self-hosted
              </span>
            </div>
            <div className="auth-study-art-foot">
              <h2>{headlines[view]}</h2>
              <p>
                deploy.example.internal
                <br />
                v0.71 · Apache-2.0
              </p>
            </div>
          </div>
        </section>
        <section className="auth-study-panel">
          <div className="auth-study-panel-top">
            {view === 'login' && (
              <>
                New here?
                <Button type="button" variant="ghost" size="sm" className="auth-study-link" onClick={() => go('signup')}>
                  Create account
                </Button>
              </>
            )}
            {view === 'setup' && 'Step 1 of 1 · owner account'}
          </div>
          <Screen view={view} go={go} key={view} />
          <div className="auth-study-legal">
            <span>Hakopod open source</span>
            <span>Privacy · Status</span>
          </div>
        </section>
      </main>
      <nav className="auth-study-review" aria-label="Design review controls">
        Review
        <select aria-label="Screen" value={view} onChange={(event) => go(event.target.value as View)}>
          {Object.entries(views).map(([id, label]) => (
            <option key={id} value={id}>
              {label}
            </option>
          ))}
        </select>
        <select aria-label="Texture" value={texture} onChange={(event) => setTexture(event.target.value as Texture)}>
          <option value="mosaic">Mosaic</option>
          <option value="field">Field</option>
        </select>
      </nav>
    </>
  )
}

document.documentElement.classList.add('light')
document.documentElement.dataset.theme = 'light'
createRoot(document.getElementById('root')!).render(<Study />)
