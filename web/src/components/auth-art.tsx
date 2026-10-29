import { useEffect, useRef, useState } from 'react'
import { Logo } from './icons'

// Layered sines: smooth, deterministic and cheap, so no noise library is needed.
const wave = (x: number, y: number, t: number) =>
  (Math.sin(x * 1.7 + t * 0.7) +
    Math.sin(y * 2.3 - t * 0.5 + x * 0.6) +
    Math.sin((x + y) * 1.1 + t * 0.3) +
    Math.sin(x * 3.9 - y * 1.3 + t)) /
  4
const ramp = [
  [12, 28, 22],
  [22, 52, 40],
  [47, 86, 65],
  [96, 170, 128],
  [159, 232, 191],
  [216, 255, 69],
]
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
      const [R, G, B] = shade(
        Math.pow(glow, 2.2) *
          (0.35 + 0.65 * Math.sin((y / h) * Math.PI)) *
          (0.55 + (0.45 * x) / w) *
          1.15,
      )
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

function Mosaic() {
  const ref = useRef<HTMLCanvasElement>(null)
  useEffect(() => {
    const canvas = ref.current
    const ctx = canvas?.getContext('2d')
    if (!canvas || !ctx) return
    const motion = matchMedia('(prefers-reduced-motion: reduce)')
    let frame = 0
    let last = 0
    let w = 0
    let h = 0
    const tick = (ms: number) => {
      // About 12 frames a second keeps the motion slow and cheap.
      if (ms - last > 80) {
        last = ms
        mosaic(ctx, w, h, ms / 4000)
      }
      frame = requestAnimationFrame(tick)
    }
    const start = () => {
      cancelAnimationFrame(frame)
      if (motion.matches) mosaic(ctx, w, h, 1.3)
      else frame = requestAnimationFrame(tick)
    }
    const size = () => {
      const rect = canvas.getBoundingClientRect()
      const dpr = Math.min(devicePixelRatio || 1, 2)
      w = Math.round(rect.width)
      h = Math.round(rect.height)
      canvas.width = w * dpr
      canvas.height = h * dpr
      ctx.setTransform(dpr, 0, 0, dpr, 0, 0)
      start()
    }
    size()
    addEventListener('resize', size)
    motion.addEventListener('change', start)
    return () => {
      cancelAnimationFrame(frame)
      removeEventListener('resize', size)
      motion.removeEventListener('change', start)
    }
  }, [])
  return <canvas ref={ref} />
}

export function AuthArt({ headline }: { headline: string }) {
  // Read after mount so server and client render the same markup.
  const [host, setHost] = useState('')
  useEffect(() => setHost(window.location.host), [])
  return (
    <section className="hako-auth-art" aria-hidden="true">
      <Mosaic />
      <div className="hako-auth-art-inner">
        <div className="hako-auth-art-top">
          <span className="hako-auth-art-brand">
            <Logo size={30} />
            hakopod
          </span>
          <span className="hako-auth-art-pill">
            <i />
            Open source · self-hosted
          </span>
        </div>
        <div className="hako-auth-art-foot">
          <h2>{headline}</h2>
          {host && <p>{host}</p>}
        </div>
      </div>
    </section>
  )
}
