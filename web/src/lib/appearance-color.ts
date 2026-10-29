export const DEFAULT_ACCENT = '#D8FF45'
export const accentPresets = [
  { name: 'Ion', color: DEFAULT_ACCENT },
  { name: 'Coral', color: '#EF816F' },
  { name: 'Violet', color: '#B5A0FA' },
  { name: 'Blue', color: '#85BCF8' },
  { name: 'Mint', color: '#72D5B3' },
] as const

export function validAccent(color: string) {
  return /^#[\da-f]{6}$/i.test(color)
}

function channels(color: string) {
  return [1, 3, 5].map((offset) => parseInt(color.slice(offset, offset + 2), 16))
}

function luminance(color: string) {
  const values = channels(color).map((channel) => {
    const value = channel / 255
    return value <= 0.04045 ? value / 12.92 : ((value + 0.055) / 1.055) ** 2.4
  })
  return values[0] * 0.2126 + values[1] * 0.7152 + values[2] * 0.0722
}

export function contrastRatio(first: string, second: string) {
  const a = luminance(first)
  const b = luminance(second)
  return (Math.max(a, b) + 0.05) / (Math.min(a, b) + 0.05)
}

export function accentForeground(color: string) {
  const ink = '#111A17'
  if (contrastRatio(color, ink) >= 4.5) return ink
  return contrastRatio(color, '#FFFFFF') >= 4.5 ? '#FFFFFF' : '#000000'
}

export function accentText(color: string, theme: 'dark' | 'light') {
  const background = theme === 'dark' ? '#1D2C26' : '#D9DFD4'
  const target = theme === 'dark' ? 255 : 0
  const original = channels(color)
  for (let step = 0; step <= 20; step++) {
    const candidate =
      '#' +
      original
        .map((value) =>
          Math.round(value + ((target - value) * step) / 20)
            .toString(16)
            .padStart(2, '0'),
        )
        .join('')
    if (contrastRatio(candidate, background) >= 4.5) return candidate.toUpperCase()
  }
  return theme === 'dark' ? '#FFFFFF' : '#000000'
}


// Cloud surface strength stays bounded so custom accents cannot obscure text.
export function cloudSurfaces(color: string, theme: 'dark' | 'light') {
  const accent = channels(validAccent(color) ? color : '#AC99FF')
  const mix = (base: number, amount: number) =>
    '#' + accent.map(value => Math.round(base * (1 - amount) + value * amount)
      .toString(16).padStart(2, '0')).join('').toUpperCase()
  return theme === 'dark'
    ? { background: mix(5, .015), card: mix(5, .04), popover: mix(7, .065),
        surface2: mix(7, .065), surface3: mix(8, .10), hairline: mix(16, .14) }
    // Light shares the warm stone set with self-hosted; the accent stays on actions only.
    : { background: '#D5CDC5', card: '#F3EFEA', popover: '#F3EFEA',
        surface2: '#CBC2B9', surface3: '#C0B6AC', hairline: '#A69D94' }
}
