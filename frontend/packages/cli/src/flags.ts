import * as z from 'zod'

export type FlagValueKind = 'string' | 'number' | 'boolean' | 'json' | 'stringArray'

export interface FlagSpec {
  path: string[]
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
  if (property.const !== undefined) {
    const kind = typeof property.const
    return kind === 'string' || kind === 'number' || kind === 'boolean' ? kind : 'json'
  }
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
  if (typeof property.const === 'string' || typeof property.const === 'number') {
    return [property.const]
  }
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

interface FlagDraft {
  path: string[]
  kind: FlagValueKind
  required: boolean
  description: string | undefined
  choices: (string | number)[] | undefined
}

function expandsToFlags(property: JsonSchema): boolean {
  return objectMembers(property, false).length > 0
}

function collectDrafts(
  drafts: Map<string, FlagDraft>,
  schema: JsonSchema,
  prefix: string[],
  conditional: boolean,
): void {
  for (const member of objectMembers(schema, conditional)) {
    const required = new Set(member.schema.required ?? [])
    for (const [key, property] of Object.entries(member.schema.properties ?? {})) {
      if (typeof property === 'boolean') continue
      const path = [...prefix, key]
      const propertyRequired = !member.conditional && required.has(key)
      if (expandsToFlags(property)) {
        collectDrafts(drafts, property, path, !propertyRequired)
        continue
      }
      const choices = enumChoices(property)
      const description = property.description?.split('\n')[0]
      const existing = drafts.get(path.join('.'))
      if (existing !== undefined) {
        existing.required = existing.required || propertyRequired
        existing.description ??= description
        existing.choices =
          existing.choices !== undefined && choices !== undefined
            ? [...new Set([...existing.choices, ...choices])]
            : undefined
        continue
      }
      drafts.set(path.join('.'), {
        path,
        kind: flagKind(property),
        required: propertyRequired,
        description,
        choices,
      })
    }
  }
}

function finalizeFlag(draft: FlagDraft): FlagSpec {
  const flag = draft.path.map(kebabCase).join('-')
  const parts: string[] = []
  if (draft.description !== undefined) parts.push(draft.description)
  if (draft.choices !== undefined) parts.push(`choices: ${draft.choices.join(', ')}`)
  if (draft.kind === 'json') parts.push('JSON value')
  if (draft.kind === 'stringArray') parts.push('repeatable')
  return {
    path: draft.path,
    flag,
    optionKey: camelCase(flag),
    kind: draft.kind,
    required: draft.required,
    description: parts.join(' — '),
  }
}

export function deriveFlags(schema: z.ZodType): FlagSpec[] {
  const jsonSchema: JsonSchema = z.toJSONSchema(schema, { io: 'input' })
  const drafts = new Map<string, FlagDraft>()
  collectDrafts(drafts, jsonSchema, [], false)
  return [...drafts.values()].map(finalizeFlag)
}
