import { useQuery } from '@tanstack/react-query'
import { APIError } from './api'
import { client, unwrap } from './client'

export const draftKey = (applicationId: string) => ['application-draft', applicationId] as const

/** The application's staged changes, or null when nothing is staged. */
export function useApplicationDraft(applicationId: string, enabled = true) {
  return useQuery({
    queryKey: draftKey(applicationId),
    queryFn: async ({ signal }) => {
      try {
        return await unwrap(
          client.GET('/applications/{id}/draft', {
            signal,
            params: { path: { id: applicationId } },
          }),
        )
      } catch (error) {
        if (error instanceof APIError && error.status === 404) return null
        throw error
      }
    },
    enabled,
    refetchInterval: 30_000,
    refetchIntervalInBackground: false,
  })
}
