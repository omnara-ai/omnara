import { readFileSync, writeFileSync } from 'node:fs'
import process from 'node:process'
import { fileURLToPath, URL } from 'node:url'

import { parse } from 'yaml'

// Idempotency describes the effect, not the response: a replay may return a
// conflict (for example, an already completed custom tool call).
export function replayContracts(spec) {
  // Observations need a bounded label for every API operation, independently of replay safety.
  const known = []
  const keyed = []
  const idempotent = []
  const parameter = (value, visited = new Set()) => {
    if (!value.$ref) return value
    const prefix = '#/components/parameters/'
    if (!value.$ref.startsWith(prefix)) throw new Error(`Unsupported parameter: ${value.$ref}`)
    if (visited.has(value.$ref)) throw new Error(`Circular parameter: ${value.$ref}`)
    visited.add(value.$ref)
    const name = value.$ref.slice(prefix.length).replaceAll('~1', '/').replaceAll('~0', '~')
    const resolved = spec.components?.parameters?.[name]
    if (!resolved) throw new Error(`Unresolved parameter: ${value.$ref}`)
    return parameter(resolved, visited)
  }
  for (const [path, item] of Object.entries(spec.paths)) {
    for (const method of ['get', 'head', 'post', 'put', 'patch', 'delete', 'options', 'trace']) {
      const operation = item[method]
      if (!operation) continue
      const parameters = [...(item.parameters ?? []), ...(operation.parameters ?? [])].map(
        (value) => parameter(value),
      )
      const hasKey = parameters.some(
        (value) => value.in === 'header' && value.name.toLowerCase() === 'idempotency-key',
      )
      const declared = operation['x-omnara-idempotent']
      const name = `${method.toUpperCase()} ${path}`
      known.push(name)
      if (declared !== undefined) {
        if (declared !== true || hasKey || method === 'get' || method === 'head') {
          throw new Error(`Invalid or redundant x-omnara-idempotent on ${name}`)
        }
        idempotent.push(name)
      }
      if (hasKey) keyed.push(name)
    }
  }
  return { known: known.sort(), keyed: keyed.sort(), idempotent: idempotent.sort() }
}

if (process.argv[1] === fileURLToPath(import.meta.url)) {
  const spec = parse(
    readFileSync(new URL('../../../../api/openapi/openapi.yaml', import.meta.url), 'utf8'),
  )
  const contracts = replayContracts(spec)
  const source = ['// Generated from api/openapi/openapi.yaml. Do not edit by hand.']
  for (const [kind, operations] of Object.entries(contracts)) {
    source.push(
      `export const ${kind}Operations: ReadonlySet<string> = new Set(${JSON.stringify(operations, null, 2)})`,
    )
  }
  writeFileSync(
    new URL('../src/generated/replay-contracts.gen.ts', import.meta.url),
    `${source.join('\n\n')}\n`,
  )
}
