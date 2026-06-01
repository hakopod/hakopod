export const validAccent = (value: string) => /^#[0-9a-f]{6}$/i.test(value)

function rgb(hex: string) {
  return [1, 3, 5].map((offset) => parseInt(hex.slice(offset, offset + 2), 16))
}
function luminance(values: number[]) {
  const linear = values.map((value) => {
    const channel = value / 255
    return channel <= 0.04045 ? channel / 12.92 : ((channel + 0.055) / 1.055) ** 2.4
  })
  return linear[0] * 0.2126 + linear[1] * 0.7152 + linear[2] * 0.0722
}
export function accentPalette(color: string, theme: string) {
  if (!validAccent(color)) return null
  const original = rgb(color)
  const dark = theme !== 'light'
  const background = luminance(rgb(dark ? '#111214' : '#faf9fc'))
  const readable = (minimum: number) => {
    let channels = original
    for (let step = 0; step <= 20; step++) {
      channels = original.map((value) =>
        Math.round(value + ((dark ? 255 : 0) - value) * (step / 20)),
      )
      const foreground = luminance(channels)
      const contrast =
        (Math.max(foreground, background) + 0.05) / (Math.min(foreground, background) + 0.05)
      if (contrast >= minimum) break
    }
    return '#' + channels.map((value) => value.toString(16).padStart(2, '0')).join('')
  }
  return {
    '--accent': readable(4.5),
    '--accent-strong': readable(5.5),
    '--accent-wash': color + (dark ? '15' : '0d'),
    '--accent-border': color + '50',
  }
}

export function applyAccent(color: string, theme: string) {
  const palette = accentPalette(color, theme)
  if (!palette) return
  for (const [name, value] of Object.entries(palette))
    document.documentElement.style.setProperty(name, value)
}
