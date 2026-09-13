import { Link } from '@tanstack/react-router'
import type { RuntimeHealth } from '../lib/runtime-health'
import type { Application } from '../lib/types'
import { timestamp } from '../lib/api'
import { Icon } from './icons'
import { Button } from './ui/button'

export function ApplicationAlarmLinks({
  application,
}: {
  application: Pick<Application, 'id' | 'project' | 'environment'>
}) {
  const search = {
    project: application.project,
    environment: application.environment,
    application_id: application.id,
  }
  return (
    <div className="runtime-issue-actions application-alarm-links">
      <Button size="sm" variant="ghost" asChild>
        <Link to="/alarms" search={search}>
          Application alarms
        </Link>
      </Button>
      <Button size="sm" variant="ghost" asChild>
        <Link to="/alarms/settings" search={search}>
          Alarm settings
        </Link>
      </Button>
    </div>
  )
}

export function RuntimeNotice({
  health,
  applicationId,
  canInspectNodes = false,
  canInspectLogs = false,
}: {
  health: RuntimeHealth
  applicationId: string
  canInspectNodes?: boolean
  canInspectLogs?: boolean
}) {
  if (!health.issues.length && !health.note) return null
  const issues = health.issues.slice(0, 3)
  return (
    <div
      className="note hako-note runtime-notice"
      role="region"
      aria-label="Runtime health"
      data-state={health.status}
    >
      <Icon name={health.issues.length ? 'alert' : 'info'} size={16} />
      <div>
        <strong>
          {health.issues.length ? `Runtime ${health.status}` : 'Runtime health unavailable'}
        </strong>
        {health.ready !== undefined && health.desired !== undefined && health.issues.length > 0 && (
          <span>
            {' '}
            · {health.ready} / {health.desired} replicas ready
          </span>
        )}
        {health.note && <p>{health.note}</p>}
        {issues.map((issue) => (
          <div className="runtime-issue" key={issue.service}>
            <p>
              <strong>{issue.service}</strong>: {issue.message}
            </p>
            <div className="runtime-issue-actions">
              <Button size="sm" variant="ghost" asChild>
                <Link
                  to="/applications/$applicationId"
                  params={{ applicationId }}
                  search={{ service: issue.service, tab: 'pods' }}
                >
                  Inspect pods
                </Link>
              </Button>
              {issue.inspect === 'nodes' && canInspectNodes && (
                <Button size="sm" variant="ghost" asChild>
                  <Link to="/infrastructure" search={{ tab: 'nodes' }}>
                    Inspect nodes
                  </Link>
                </Button>
              )}
              {issue.inspect === 'logs' && canInspectLogs && (
                <Button size="sm" variant="ghost" asChild>
                  <Link
                    to="/applications/$applicationId"
                    params={{ applicationId }}
                    search={{ service: issue.service, tab: 'logs' }}
                  >
                    Inspect logs
                  </Link>
                </Button>
              )}
            </div>
          </div>
        ))}
        {health.issues.length > issues.length && (
          <p>{health.issues.length - issues.length} more services need inspection.</p>
        )}
        {health.observedAt && <small>Observed {timestamp(health.observedAt)}</small>}
      </div>
    </div>
  )
}
