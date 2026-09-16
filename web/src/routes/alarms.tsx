import { useState } from 'react'
import { createFileRoute, Link, Outlet, useLocation } from '@tanstack/react-router'
import { useQueryClient } from '@tanstack/react-query'
import { Status as HatchStatus } from '@hakopod/hatch-ui/components/status'
import { alarmSearch, useAlarms, type Alarm, type AlarmSearch } from '../lib/alarms'
import { useProjects } from '../lib/projects'
import { client, unwrap } from '../lib/client'
import { message, timestamp } from '../lib/api'
import { Button } from '../components/ui/button'
import { SelectField } from '../components/ui/select'
import { Empty, ErrorState, Loading, PageHeader, RequestError } from '../components/shared'

export const Route = createFileRoute('/alarms')({
  validateSearch: alarmSearch,
  component: AlarmsRoute,
})

function AlarmsRoute() {
  const search = Route.useSearch()
  const path = useLocation().pathname
  return path === '/alarms' ? <Alarms key={JSON.stringify(search)} /> : <Outlet />
}

function Alarms() {
  const search = Route.useSearch()
  const navigate = Route.useNavigate()
  const projects = useProjects()
  const [page, setPage] = useState({ cursor: '', previous: [] as string[] })
  const alarms = useAlarms(search, page.cursor)
  const project = projects.data?.items.find((item) => item.name === search.project)
  const change = (next: AlarmSearch) => {
    setPage({ cursor: '', previous: [] })
    void navigate({ search: next })
  }
  return (
    <div className="ops-page alarms-page">
      <PageHeader
        title="Alarms"
        description="Sustained runtime problems and their recovery. Acknowledging an alarm records that you have seen it; it does not fix the resource."
        action={
          <div className="toolbar-actions">
            <Button size="sm" disabled={alarms.isFetching} onClick={() => void alarms.refetch()}>
              Refresh
            </Button>
            <Button asChild size="sm">
              <Link
                to="/alarms/settings"
                search={{
                  project: search.project,
                  environment: search.environment,
                  application_id: search.application_id,
                }}
              >
                Alarm settings
              </Link>
            </Button>
          </div>
        }
      />
      <div className="alarm-toolbar">
        <div className="alarm-filters">
          <SelectField
            compact
            label="Alarm state"
            value={search.status || ''}
            onValueChange={(value) =>
              change({
                ...search,
                status: value === 'active' || value === 'recovered' ? value : undefined,
              })
            }
            options={[
              { value: '', label: 'All states' },
              { value: 'active', label: 'Active' },
              { value: 'recovered', label: 'Recovered' },
            ]}
          />
          <SelectField
            compact
            label="Alarm project"
            value={search.project || ''}
            onValueChange={(value) =>
              change({ status: search.status, project: value || undefined })
            }
            options={[
              { value: '', label: 'All projects' },
              ...(search.project && !project
                ? [{ value: search.project, label: search.project }]
                : []),
              ...(projects.data?.items.map((item) => ({
                value: item.name,
                label: item.display_name || item.name,
              })) || []),
            ]}
          />
          {search.project && (
            <SelectField
              compact
              label="Alarm environment"
              value={search.environment || ''}
              onValueChange={(value) =>
                change({
                  project: search.project,
                  status: search.status,
                  environment: value || undefined,
                })
              }
              options={[
                { value: '', label: 'All environments' },
                ...(search.environment &&
                !project?.environments.some((item) => item.name === search.environment)
                  ? [{ value: search.environment, label: search.environment }]
                  : []),
                ...(project?.environments.map((item) => ({ value: item.name, label: item.name })) ||
                  []),
              ]}
            />
          )}
          {search.application_id && (
            <Button
              size="sm"
              variant="ghost"
              onClick={() => change({ ...search, application_id: undefined })}
            >
              Clear application filter
            </Button>
          )}
        </div>
        {alarms.data && !alarms.error && (
          <span className="alarm-counts">
            {alarms.data.summary.active} active · {alarms.data.summary.unread} unread
          </span>
        )}
      </div>
      {projects.error && (
        <ErrorState error={projects.error} retry={() => void projects.refetch()} />
      )}
      {alarms.isPending ? (
        <Loading />
      ) : alarms.error ? (
        <ErrorState error={alarms.error} retry={() => void alarms.refetch()} />
      ) : !alarms.data?.items.length ? (
        <Empty
          icon="check"
          title="No alarms in this view"
          description="Alarms appear after a problem persists for the configured hold time. No alarms does not guarantee that every resource is healthy."
        />
      ) : (
        <div className="alarm-list" aria-label="Alarm inbox">
          {alarms.data.items.map((alarm) => (
            <AlarmRow key={alarm.id} alarm={alarm} />
          ))}
        </div>
      )}
      {(page.previous.length > 0 || alarms.data?.next_cursor) && (
        <div className="table-pagination">
          <Button
            size="sm"
            disabled={!page.previous.length || alarms.isFetching}
            onClick={() =>
              setPage({ cursor: page.previous.at(-1) || '', previous: page.previous.slice(0, -1) })
            }
          >
            Previous
          </Button>
          <span>Page {page.previous.length + 1}</span>
          <Button
            size="sm"
            disabled={!alarms.data?.next_cursor || page.previous.length >= 39 || alarms.isFetching}
            onClick={() =>
              setPage({
                cursor: alarms.data!.next_cursor,
                previous: [...page.previous, page.cursor],
              })
            }
          >
            Next
          </Button>
          {page.previous.length >= 39 && <span>Narrow the filters to browse more alarms.</span>}
        </div>
      )}
    </div>
  )
}

function AlarmRow({ alarm }: { alarm: Alarm }) {
  const cache = useQueryClient()
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const update = async (action: 'read' | 'acknowledge') => {
    if (busy) return
    setBusy(true)
    setError('')
    try {
      await unwrap(
        client.POST(action === 'read' ? '/alarms/{id}/read' : '/alarms/{id}/acknowledge', {
          params: { path: { id: alarm.id } },
          body: { expected_event_id: alarm.last_event_id },
        }),
      )
      await cache.invalidateQueries({ queryKey: ['alarms'] })
    } catch (err) {
      setError(message(err))
    } finally {
      setBusy(false)
    }
  }
  return (
    <article
      className="alarm-row"
      data-unread={!alarm.read || undefined}
      aria-label={`${alarm.resource_name}: ${alarm.status}`}
    >
      <div className="alarm-row-main">
        <div className="alarm-row-title">
          <HatchStatus tone={alarm.status === 'active' ? 'error' : 'success'}>
            {alarm.status === 'active' ? 'Active' : 'Recovered'}
          </HatchStatus>
          <strong>{alarm.resource_name}</strong>
          {!alarm.read && <span className="alarm-unread-label">Unread</span>}
          {alarm.acknowledged && <span className="field-help">Acknowledged</span>}
        </div>
        <p>{alarm.summary}</p>
        <div className="alarm-row-meta">
          <span>
            {alarm.project
              ? [alarm.project, alarm.environment, alarm.service].filter(Boolean).join(' / ')
              : 'Installation'}
          </span>
          <span>Fired {timestamp(alarm.fired_at)}</span>
          {alarm.recovered_at && <span>Recovered {timestamp(alarm.recovered_at)}</span>}
          <span>Observed {timestamp(alarm.last_observed_at)}</span>
        </div>
        {alarm.observation_status === 'unknown' && (
          <p className="alarm-observation-warning">
            {alarm.status === 'active'
              ? 'Current health is unknown. Recovery has not been confirmed.'
              : 'This rule was resolved without a healthy observation. See the resolution above.'}
          </p>
        )}
        {error && <RequestError error={error} />}
      </div>
      <div className="alarm-row-actions">
        <Button asChild size="sm" variant="ghost">
          {alarm.resource_type === 'node' ? (
            <Link to="/infrastructure" search={{ tab: 'nodes' }}>
              Inspect node
            </Link>
          ) : (
            <Link
              to="/applications/$applicationId"
              params={{ applicationId: alarm.application_id }}
              search={{
                service: alarm.service || undefined,
                tab: alarm.service ? 'pods' : 'services',
              }}
            >
              Inspect {alarm.service ? 'service' : 'application'}
            </Link>
          )}
        </Button>
        {!alarm.read && (
          <Button size="sm" variant="ghost" disabled={busy} onClick={() => void update('read')}>
            Mark as read
          </Button>
        )}
        {alarm.status === 'active' && !alarm.acknowledged && (
          <Button size="sm" disabled={busy} onClick={() => void update('acknowledge')}>
            {busy ? 'Saving…' : 'Acknowledge'}
          </Button>
        )}
      </div>
    </article>
  )
}
