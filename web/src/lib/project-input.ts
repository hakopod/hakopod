export const projectIDPattern = /^[a-z](?:[a-z0-9-]{0,38}[a-z0-9])?$/
export const projectIDHelp =
  'Use 1–40 lowercase letters, digits, and hyphens. Start with a letter and end with a letter or digit.'

export function projectIDFromName(name: string) {
  let id = name
    .normalize('NFKD')
    .replace(/[\u0300-\u036f]/g, '')
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, '-')
    .replace(/^-+|-+$/g, '')
  if (id && !/^[a-z]/.test(id)) id = `project-${id}`
  return id.slice(0, 40).replace(/-+$/g, '')
}

export function projectInputErrors(input: {
  displayName: string
  id: string
  description: string
  environment: string
}) {
  return {
    displayName: !input.displayName.trim()
      ? 'Required'
      : input.displayName.trim().length > 80
        ? 'Use up to 80 characters.'
        : '',
    id: projectIDPattern.test(input.id) ? '' : projectIDHelp,
    description: input.description.trim().length > 1000 ? 'Use up to 1,000 characters.' : '',
    environment: projectIDPattern.test(input.environment) ? '' : projectIDHelp,
  }
}
