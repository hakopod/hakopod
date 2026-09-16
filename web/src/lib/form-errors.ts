// Only associate errors with fields explicitly identified by the API. General
// permission, capacity and network errors remain form-level banners.
export function fieldError(error: string, ...paths: string[]) {
  for (const line of validationLines(error)) {
    for (const path of paths) {
      if (line.startsWith(`${path}:`)) return line.slice(path.length + 1).trim()
    }
  }
  return undefined
}

export function validationLines(error: string) {
  // Go's input-error sentinel wraps explicit paths in API responses.
  return error.split('\n').map((line) => line.replace(/^invalid input: /, ''))
}

export function fieldGroupError(error: string, path: string) {
  return (
    fieldError(error, path) ||
    validationLines(error).find((line) => line.startsWith(`${path}.`) && line.includes(':'))
  )
}
