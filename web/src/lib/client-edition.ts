// Editions can bind requests to the document's workspace without exposing tokens.
export function editionFetch(input: RequestInfo | URL, init?: RequestInit): Promise<Response> {
  return fetch(input, init)
}
