import { readFile, writeFile } from 'node:fs/promises'

// Keep editor guidance on the same contract as the API and typed clients.
const document = JSON.parse(await readFile(new URL('../../api/openapi.json', import.meta.url), 'utf8'))
const schemas = document.components?.schemas
if (!schemas?.Spec) throw new Error('OpenAPI does not define the application Spec schema.')

const prefix = '#/components/schemas/'
const names = new Set(['Spec'])
const pending = ['Spec']
function collectReferences(value) {
  if (!value || typeof value !== 'object') return
  if (Array.isArray(value)) {
    value.forEach(collectReferences)
    return
  }
  if (typeof value.$ref === 'string') {
    if (!value.$ref.startsWith(prefix)) throw new Error(`Unsupported editor schema reference: ${value.$ref}`)
    const name = value.$ref.slice(prefix.length)
    if (!Object.hasOwn(schemas, name)) throw new Error(`Missing editor schema reference: ${value.$ref}`)
    if (!names.has(name)) {
      names.add(name)
      pending.push(name)
    }
  }
  Object.values(value).forEach(collectReferences)
}
for (let index = 0; index < pending.length; index++) collectReferences(schemas[pending[index]])

const ordered = ['Spec', ...[...names].filter((name) => name !== 'Spec').sort()]
const catalog = Object.fromEntries(ordered.map((name) => [name, schemas[name]]))
await writeFile(new URL('../src/lib/editor-schema.json', import.meta.url), `${JSON.stringify(catalog, null, 2)}\n`)
console.log(`Generated editor guidance for ${ordered.length} application schemas.`)
