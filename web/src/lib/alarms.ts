import { useQuery } from '@tanstack/react-query'
import { client, unwrap } from './client'
import type { components } from './api.generated'

export type Alarm = components['schemas']['Alarm']
export type AlarmSettings = components['schemas']['AlarmSettings']
export type AlarmSettingsInput = components['schemas']['AlarmSettingsInput']
export type AlarmScope = { project?: string; environment?: string; application_id?: string }
export type AlarmSearch = AlarmScope & { status?: 'active' | 'recovered' }

export function alarmSearch(search: Record<string, unknown>): AlarmSearch {
  return {
    ...Object.fromEntries(
      ['project', 'environment', 'application_id'].flatMap((key) =>
        typeof search[key] === 'string' && search[key] ? [[key, search[key]]] : [],
      ),
    ),
    status: search.status === 'active' || search.status === 'recovered' ? search.status : undefined,
  }
}

export function useAlarms(search: AlarmSearch = {}, cursor = '', limit = 25) {
  return useQuery({
    queryKey: ['alarms', search, cursor, limit],
    queryFn: ({ signal }) =>
      unwrap(
        client.GET('/alarms', {
          signal,
          params: { query: { ...search, cursor: cursor || undefined, limit } },
        }),
      ),
    staleTime: 15000,
    refetchInterval: 30000,
    refetchIntervalInBackground: false,
    gcTime: 0,
    retry: false,
  })
}

export function useAlarmSettings(scope: AlarmScope, enabled: boolean) {
  return useQuery({
    queryKey: ['alarm-settings', scope],
    queryFn: ({ signal }) =>
      unwrap(client.GET('/alarm-settings', { signal, params: { query: scope } })),
    enabled,
    gcTime: 0,
    retry: false,
  })
}
