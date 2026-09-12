import type { CSSProperties } from 'react'
import {
  Activity,
  Archive,
  ArrowLeft,
  ArrowRight,
  BookOpen,
  Box,
  Check,
  ChevronDown,
  ChevronRight,
  CircleAlert,
  Copy,
  Database,
  ExternalLink,
  GitBranch,
  Globe,
  Grid2X2,
  Info,
  KeyRound,
  Lock,
  LogOut,
  Menu,
  Moon,
  Network,
  Pause,
  Play,
  Plus,
  RefreshCw,
  Search,
  Server,
  Settings,
  ShieldCheck,
  Sun,
  Terminal,
  User,
  X,
  Clock,
  Code,
} from 'lucide-react'
const icons = {
  database: Database,
  archive: Archive,
  user: User,
  grid: Grid2X2,
  box: Box,
  server: Server,
  activity: Activity,
  settings: Settings,
  plus: Plus,
  search: Search,
  chevron: ChevronRight,
  down: ChevronDown,
  arrow: ArrowRight,
  back: ArrowLeft,
  external: ExternalLink,
  refresh: RefreshCw,
  check: Check,
  x: X,
  clock: Clock,
  globe: Globe,
  lock: Lock,
  key: KeyRound,
  network: Network,
  code: Code,
  terminal: Terminal,
  copy: Copy,
  sun: Sun,
  moon: Moon,
  info: Info,
  alert: CircleAlert,
  logout: LogOut,
  book: BookOpen,
  branch: GitBranch,
  pause: Pause,
  play: Play,
  menu: Menu,
  shield: ShieldCheck,
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
  const Glyph = icons[name as keyof typeof icons] || Box
  return (
    <Glyph
      size={size}
      strokeWidth={1.75}
      aria-hidden="true"
      className={className}
      style={{ width: size, height: size, ...style }}
    />
  )
}
export function Logo({ size = 30 }: { size?: number }) {
  return (
    <svg
      width={size}
      height={size}
      style={{ width: size, height: size }}
      viewBox="0 0 96 96"
      fill="currentColor"
      aria-hidden="true"
      className="brand-mark"
    >
      <rect x="42" y="6" width="12" height="36" rx="1.5" />
      <rect x="42" y="54" width="12" height="36" rx="1.5" />
      <rect x="18" y="18" width="12" height="24" rx="1.5" />
      <rect x="18" y="54" width="12" height="24" rx="1.5" />
      <rect x="66" y="18" width="12" height="24" rx="1.5" />
      <rect x="66" y="54" width="12" height="24" rx="1.5" />
      <rect x="6" y="42" width="12" height="12" rx="1.5" />
      <rect x="78" y="42" width="12" height="12" rx="1.5" />
    </svg>
  )
}
