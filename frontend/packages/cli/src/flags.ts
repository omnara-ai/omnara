import * as z from 'zod'

export type FlagValueKind = 'string' | 'number' | 'boolean' | 'json' | 'stringArray'

export interface FlagSpec {
  key: string
  flag: string
  optionKey: string
  kind: FlagValueKind
  required: boolean
  description: string
}

type JsonSchema = z.core.JSONSchema.BaseSchema

export function kebabCase(name: string): string {
  return name
    .replace(/([a-z0-9])([A-Z])/g, '$1-$2')
    .replaceAll('_', '-')
    .toLowerCase()
}

function camelCase(flag: string): string {
  return flag.replace(/-(\w)/g, (_, letter: string) => letter.toUpperCase())
}

interface ObjectMember {
  schema: JsonSchema
  conditional: boolean
}

function objectMembers(schema: JsonSchema, conditional: boolean): ObjectMember[] {
  const members: ObjectMember[] = []
  if (schema.properties !== undefined) members.push({ schema, conditional })
  for (const member of schema.allOf ?? []) {
    members.push(...objectMembers(member, conditional))
  }
  for (const variants of [schema.anyOf, schema.oneOf]) {
    for (const variant of variants ?? []) {
      members.push(...objectMembers(variant, true))
    }
  }
  return members
}

function nonNullVariants(property: JsonSchema): JsonSchema[] | undefined {
  const variants = property.anyOf ?? property.oneOf
  if (variants === undefined) return undefined
  return variants.filter((variant) => variant.type !== 'null')
}

function flagKind(property: JsonSchema): FlagValueKind {
  if (property.enum !== undefined) return 'string'
  const variants = nonNullVariants(property)
  if (variants !== undefined) {
    const kinds = new Set(variants.map(flagKind))
    const [first] = kinds
    return kinds.size === 1 && first !== undefined ? first : 'json'
  }
  switch (property.type) {
    case 'string':
      return 'string'
    case 'integer':
    case 'number':
      return 'number'
    case 'boolean':
      return 'boolean'
    case 'array': {
      const items = property.items
      return typeof items === 'object' &&
        !Array.isArray(items) &&
        items.type === 'string' &&
        items.enum === undefined
        ? 'stringArray'
        : 'json'
    }
    default:
      return 'json'
  }
}

function enumChoices(property: JsonSchema): (string | number)[] | undefined {
  if (property.enum !== undefined) {
    const choices = property.enum.filter(
      (value) => typeof value === 'string' || typeof value === 'number',
    )
    return choices.length > 0 ? choices : undefined
  }
  const variants = nonNullVariants(property)
  const only = variants?.length === 1 ? variants[0] : undefined
  return only === undefined ? undefined : enumChoices(only)
}

function flagDescription(property: JsonSchema, kind: FlagValueKind): string {
  const parts: string[] = []
  if (property.description !== undefined) parts.push(property.description.split('\n')[0] ?? '')
  const choices = enumChoices(property)
  if (choices !== undefined) parts.push(`choices: ${choices.join(', ')}`)
  if (kind === 'json') parts.push('JSON value')
  if (kind === 'stringArray') parts.push('repeatable')
  return parts.join(' — ')
}

export function deriveFlags(schema: z.ZodType): FlagSpec[] {
  const jsonSchema: JsonSchema = z.toJSONSchema(schema, { io: 'input' })
  const flags = new Map<string, FlagSpec>()
  for (const member of objectMembers(jsonSchema, false)) {
    const required = new Set(member.schema.required ?? [])
    for (const [key, property] of Object.entries(member.schema.properties ?? {})) {
      if (typeof property === 'boolean') continue
      const memberRequired = !member.conditional && required.has(key)
      const existing = flags.get(key)
      if (existing !== undefined) {
        existing.required = existing.required || memberRequired
        continue
      }
      const kind = flagKind(property)
      const flag = kebabCase(key)
      flags.set(key, {
        key,
        flag,
        optionKey: camelCase(flag),
        kind,
        required: memberRequired,
        description: flagDescription(property, kind),
      })
    }
  }
  return [...flags.values()]
}
