// Only associate errors with fields explicitly identified by the API. General
// permission, capacity and network errors remain form-level banners.
export function fieldError(error: string, ...paths: string[]) {
  for (const line of error.split('\n')) {
    for (const path of paths) {
      if (line.startsWith(`${path}:`)) return line.slice(path.length + 1).trim()
    }
  }
  return undefined
}
