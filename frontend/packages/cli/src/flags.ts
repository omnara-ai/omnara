import * as z from 'zod'
import type { RenderValue } from './output.ts'

export type FlagValueKind = 'string' | 'number' | 'boolean' | 'json' | 'stringArray'

export interface FlagSpec {
  key: string
  flag: string
  optionKey: string
  kind: FlagValueKind
  required: boolean
  description: string
}

type JsonSchema = Record<string, RenderValue>

function kebabCase(key: string): string {
  return key.replaceAll('_', '-')
}

function camelCase(flag: string): string {
  return flag.replace(/-(\w)/g, (_, letter: string) => letter.toUpperCase())
}

function objectMembers(schema: JsonSchema): JsonSchema[] {
  if (Array.isArray(schema.allOf)) {
    return (schema.allOf as JsonSchema[]).flatMap(objectMembers)
  }
  if (schema.properties) return [schema]
  return []
}

function flagKind(property: JsonSchema): FlagValueKind {
  if (Array.isArray(property.enum)) return 'string'
  switch (property.type) {
    case 'string':
      return 'string'
    case 'integer':
    case 'number':
      return 'number'
    case 'boolean':
      return 'boolean'
    case 'array': {
      const items = property.items as JsonSchema | undefined
      return items?.type === 'string' && !Array.isArray(items.enum) ? 'stringArray' : 'json'
    }
    default:
      return 'json'
  }
}

function flagDescription(property: JsonSchema, kind: FlagValueKind): string {
  const parts: string[] = []
  if (typeof property.description === 'string') parts.push(property.description.split('\n')[0] ?? '')
  if (Array.isArray(property.enum)) parts.push(`choices: ${property.enum.join(', ')}`)
  if (kind === 'json') parts.push('JSON value')
  if (kind === 'stringArray') parts.push('repeatable')
  return parts.join(' — ')
}

export function deriveFlags(schema: z.ZodType): FlagSpec[] {
  const jsonSchema = z.toJSONSchema(schema, { io: 'input' }) as JsonSchema
  const flags = new Map<string, FlagSpec>()
  for (const member of objectMembers(jsonSchema)) {
    const properties = member.properties as Record<string, JsonSchema>
    const required = new Set((member.required as string[] | undefined) ?? [])
    for (const [key, property] of Object.entries(properties)) {
      if (flags.has(key)) continue
      const kind = flagKind(property)
      const flag = kebabCase(key)
      flags.set(key, {
        key,
        flag,
        optionKey: camelCase(flag),
        kind,
        required: required.has(key),
        description: flagDescription(property, kind),
      })
    }
  }
  return [...flags.values()]
}
