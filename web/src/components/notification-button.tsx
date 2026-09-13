import { Link, useLocation } from '@tanstack/react-router'
import { Bell } from 'lucide-react'
import { useAlarms } from '../lib/alarms'
import { Button } from './ui/button'
import { Tooltip } from './ui/surfaces'

export function NotificationButton() {
  const active = useLocation().pathname.startsWith('/alarms')
  const alarms = useAlarms({}, '', 1)
  const summary = alarms.data?.summary
  const detail = alarms.error
    ? 'Notifications unavailable. Open to retry.'
    : summary
      ? `${summary.unread} unread · ${summary.active} active alarms`
      : 'Loading notifications'
  return (
    <Tooltip content={detail}>
      <Button asChild variant="ghost" size="icon" className="alarm-notification-button">
        <Link
          to="/alarms"
          aria-label="Notifications"
          title={detail}
          data-active={active || undefined}
        >
          <Bell size={18} aria-hidden="true" />
          {!alarms.error && Boolean(summary?.unread) && (
            <span className="alarm-unread-dot" aria-hidden="true" />
          )}
          {alarms.error && (
            <span className="alarm-notification-error" aria-hidden="true">
              !
            </span>
          )}
        </Link>
      </Button>
    </Tooltip>
  )
}
