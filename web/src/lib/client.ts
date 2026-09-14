import { editionFetch } from './client-edition'
import createClient from 'openapi-fetch'
import type { paths } from './api.generated'
import { APIError } from './api'

// OpenAPI-generated route/body/response types, through the same-origin cookie proxy.
// This typed client is shared by queries; the browser never handles a bearer token.
export const client = createClient<paths>({
  baseUrl: '/api',
  fetch: editionFetch,
  credentials: 'same-origin',
  cache: 'no-store',
})
export async function unwrap<T>(
  request: Promise<{
    data?: T
    error?: { error: { message: string; code?: string } }
    response: Response
  }>,
): Promise<T> {
  const result = await request
  if (result.error || !result.response.ok) {
    throw new APIError(
      result.error?.error.message || `Request failed (${result.response.status}).`,
      result.response.status,
      result.error?.error.code,
    )
  }
  return result.data as T
}
