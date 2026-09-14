// Only server-side edition code may choose a workspace API transport.
export function editionHeaders(_request: Request): Record<string, string> {
  return {}
}

export function editionSignOutCookies(_request: Request): string[] {
  return []
}

export function editionRequestError(_request: Request, _path: string): Response | null {
  return null
}
export function editionResponseHeaders(_request: Request): Record<string, string> {
  return {}
}
