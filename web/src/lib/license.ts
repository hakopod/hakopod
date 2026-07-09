import { useQuery } from '@tanstack/react-query'
import { client, unwrap } from './client'
export function useLicense() {
  return useQuery({
    queryKey: ['license'],
    queryFn: ({ signal }) => unwrap(client.GET('/license', { signal })),
    staleTime: 60000,
  })
}
