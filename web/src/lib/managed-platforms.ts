import { useQuery } from '@tanstack/react-query'
import { client, unwrap } from './client'
import type { components } from './api.generated'

export type ManagedPlatform = components['schemas']['ManagedPlatform']
export type ManagedPlatformOperation = components['schemas']['ManagedPlatformOperation']
export type ManagedPlatformSpec = components['schemas']['ManagedPlatformSpec']
export type ManagedPlatformDefaults = components['schemas']['ManagedPlatformDefaults']
export type ManagedPlatformCatalog = components['schemas']['ManagedPlatformCatalog']
export type ManagedPlatformCatalogEntry = components['schemas']['ManagedPlatformCatalogEntry']
export type ManagedPlatformReviewResponse = components['schemas']['ManagedPlatformReviewResponse']
export const managedPlatformName = (kind: string) => kind === 'neon' ? 'Neon' : kind === 'supabase' ? 'Supabase' : 'Managed platform'
export const platformSearch = (search: Record<string, unknown>) => ({ project: typeof search.project === 'string' ? search.project : '', environment: typeof search.environment === 'string' ? search.environment : '' })
export function useManagedPlatforms(project: string, environment: string) { return useQuery({ queryKey: ['managed-platforms', project, environment], queryFn: ({signal}) => unwrap(client.GET('/managed-platforms', { signal, params: { query: project || environment ? { project, environment } : {} } })), refetchInterval: 5000, gcTime: 0 }) }
export function useManagedPlatformCatalog(project: string, environment: string, enabled = true) { return useQuery({ queryKey: ['managed-platform-catalog', project, environment], queryFn: ({signal}) => unwrap(client.GET('/managed-platforms/catalog', { signal, params: { query: { project, environment } } })), enabled: Boolean(enabled && project && environment), staleTime: 30_000, gcTime: 0 }) }
export function useManagedPlatform(id: string) { return useQuery({ queryKey: ['managed-platform', id], queryFn: ({signal}) => unwrap(client.GET('/managed-platforms/{id}', { signal, params: { path: { id } } })), enabled: Boolean(id), refetchInterval: 5000, gcTime: 0 }) }
export function useManagedPlatformOperations(id: string) { return useQuery({ queryKey: ['managed-platform-operations', id], queryFn: ({signal}) => unwrap(client.GET('/managed-platforms/{id}/operations', { signal, params: { path: { id } } })), enabled: Boolean(id), refetchInterval: 5000, gcTime: 0 }) }
