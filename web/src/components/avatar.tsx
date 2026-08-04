import { useState } from 'react'
export function avatarURL(style: string, seed: string) {
  return ['identicon', 'glass'].includes(style)
    ? `https://api.dicebear.com/10.x/${style}/svg?seed=${encodeURIComponent(seed)}&size=96`
    : ''
}
export function Avatar({ name, url, size = 34 }: { name: string; url?: string; size?: number }) {
  const [failed, setFailed] = useState('')
  const valid = Boolean(
    url && /^https:\/\/api\.dicebear\.com\/10\.x\/(?:identicon|glass)\/svg\?/.test(url),
  )
  const initials =
    name
      .trim()
      .split(/\s+/)
      .slice(0, 2)
      .map((part) => part[0] || '')
      .join('')
      .toUpperCase() || 'H'
  return (
    <span
      className="profile-avatar"
      style={{ width: size, height: size, fontSize: Math.round(size * 0.34) }}
      aria-hidden="true"
    >
      {valid && failed !== url ? (
        <img
          src={url}
          width={size}
          height={size}
          alt=""
          loading="lazy"
          decoding="async"
          crossOrigin="anonymous"
          referrerPolicy="no-referrer"
          onError={() => setFailed(url || '')}
        />
      ) : (
        initials
      )}
    </span>
  )
}
