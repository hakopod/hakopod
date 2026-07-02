import { BrandMark } from '@hakopod/ui'
import type { CSSProperties } from 'react'
const paths: Record<string, string> = {
  grid: 'M3 3h7v7H3zM14 3h7v7h-7zM3 14h7v7H3zM14 14h7v7h-7z',
  box: 'm12 3 9 5-9 5-9-5 9-5ZM3 8v9l9 5 9-5V8M12 13v9M7.5 5.5l9 5',
  server: 'M4 3h16v7H4zM4 14h16v7H4zM7 6.5h.01M7 17.5h.01M11 6.5h6M11 17.5h6',
  activity: 'M3 12h4l3-8 4 16 3-8h4',
  settings:
    'M12 8a4 4 0 1 0 0 8 4 4 0 0 0 0-8ZM12 2v3M12 19v3M2 12h3M19 12h3M5 5l2 2M17 17l2 2M5 19l2-2M17 7l2-2',
  plus: 'M12 5v14M5 12h14',
  search: 'M21 21l-5-5M10.5 3a7.5 7.5 0 1 0 0 15 7.5 7.5 0 0 0 0-15Z',
  chevron: 'm9 5 7 7-7 7',
  down: 'm6 9 6 6 6-6',
  arrow: 'M4 12h16m-6-6 6 6-6 6',
  back: 'M20 12H4m6-6-6 6 6 6',
  external: 'M14 3h7v7m0-7-11 11M10 3H3v18h18v-7',
  refresh: 'M20 7A9 9 0 1 0 21 14M20 3v5h-5',
  check: 'm5 12 4 4L19 6',
  x: 'm6 6 12 12M6 18 18 6',
  clock: 'M12 3a9 9 0 1 0 0 18 9 9 0 0 0 0-18ZM12 7v5l3 2',
  globe: 'M12 3a9 9 0 1 0 0 18 9 9 0 0 0 0-18ZM3 12h18M12 3a17 17 0 0 0 0 18 17 17 0 0 0 0-18Z',
  lock: 'M5 10h14v11H5zM8 10V7a4 4 0 0 1 8 0v3M12 14v3',
  key: 'M14 3a7 7 0 0 0-6 10L2 19v3h4v-3h3v-3l2-2a7 7 0 1 0 3-11ZM16 7h.01',
  network: 'M9 2h6v6H9zM2 16h6v6H2zM16 16h6v6h-6zM12 8v4M5 16v-4h14v4',
  code: 'm8 5-7 7 7 7M16 5l7 7-7 7M14 3l-4 18',
  terminal: 'm4 5 6 6-6 6M13 18h7',
  copy: 'M9 9h12v12H9zM5 15H3V3h12v2',
  sun: 'M12 8a4 4 0 1 0 0 8 4 4 0 0 0 0-8ZM12 1v3M12 20v3M1 12h3M20 12h3M4 4l2 2M18 18l2 2M4 20l2-2M18 6l2-2',
  moon: 'M21 13A9 9 0 0 1 11 3a9 9 0 1 0 10 10Z',
  info: 'M12 3a9 9 0 1 0 0 18 9 9 0 0 0 0-18ZM12 11v6M12 7h.01',
  alert: 'm12 3 10 18H2L12 3ZM12 9v5M12 17h.01',
  logout: 'M9 3H3v18h6M10 12h11m-5-5 5 5-5 5',
  book: 'M12 5v16M3 3h5l4 2 4-2h5v16h-5l-4 2-4-2H3V3Z',
  branch:
    'M6 7v10M18 7a6 6 0 0 1-6 6H6M6 2a2.5 2.5 0 1 0 0 5 2.5 2.5 0 0 0 0-5ZM6 17a2.5 2.5 0 1 0 0 5 2.5 2.5 0 0 0 0-5ZM18 2a2.5 2.5 0 1 0 0 5 2.5 2.5 0 0 0 0-5Z',
  pause: 'M7 5v14M17 5v14',
  play: 'm7 3 14 9-14 9V3Z',
  menu: 'M3 6h18M3 12h18M3 18h18',
  shield: 'm12 2 9 4v6c0 5-9 10-9 10S3 17 3 12V6l9-4ZM8 12l3 3 5-6',
}
export function Icon({
  name,
  size = 18,
  className,
  style,
}: {
  name: string
  size?: number
  className?: string
  style?: CSSProperties
}) {
  return (
    <svg
      width={size}
      height={size}
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.6"
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden="true"
      className={className}
      style={style}
    >
      <path d={paths[name] || paths.box} />
    </svg>
  )
}
export function Logo({ size = 30 }: { size?: number }) {
  return <BrandMark size={size} className="brand-mark" />
}
