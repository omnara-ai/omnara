import type { AppCapabilityDefinition, AppCronTriggerTarget } from '@omnara/sdk'
import { z } from 'zod'

export const appScheduleSettings = z.record(z.string(), z.json())

const stringField = z.strictObject({
  type: z.literal('string'),
  title: z.string().optional(),
  description: z.string().optional(),
  default: z.string().optional(),
  pattern: z.string().optional(),
  minLength: z.number().int().nonnegative().optional(),
  maxLength: z.number().int().nonnegative().optional(),
  'x-omnara-control': z.enum(['agent_profile', 'textarea']).optional(),
})

const simpleSchema = z.strictObject({
  $schema: z.string().optional(),
  type: z.literal('object'),
  title: z.string().optional(),
  description: z.string().optional(),
  properties: z.record(z.string(), stringField),
  required: z.array(z.string()).default([]),
  additionalProperties: z.literal(false),
  default: appScheduleSettings.optional(),
  'x-omnara-field-order': z.array(z.string()).default([]),
})

export function appScheduleDefaults(
  schema: AppCapabilityDefinition['input_schema'],
): AppCronTriggerTarget['settings'] {
  const rootDefault = appScheduleSettings.safeParse(schema.default)
  if (rootDefault.success) return rootDefault.data
  const properties = z
    .record(z.string(), z.object({ default: z.json().optional() }))
    .safeParse(schema.properties)
  if (!properties.success) return {}
  return Object.fromEntries(
    Object.entries(properties.data).flatMap(([key, property]) =>
      property.default === undefined ? [] : [[key, property.default]],
    ),
  )
}

export function appScheduleFields(
  schema: AppCapabilityDefinition['input_schema'],
  settings: AppCronTriggerTarget['settings'],
) {
  const parsed = simpleSchema.safeParse(schema)
  if (!parsed.success) return null
  const strings = z.record(z.string(), z.string()).safeParse(settings)
  if (!strings.success) return null
  const { properties, required, 'x-omnara-field-order': order } = parsed.data
  if ([...Object.keys(settings), ...required].some((key) => !Object.hasOwn(properties, key)))
    return null
  return Object.entries(properties)
    .map(([key, property]) => ({
      key,
      property,
      required: required.includes(key),
      value: strings.data[key],
    }))
    .sort((a, b) => {
      const index = (key: string) => (order.includes(key) ? order.indexOf(key) : order.length)
      return index(a.key) - index(b.key)
    })
}

export function appScheduleFieldError(
  value: string | undefined,
  property: z.infer<typeof stringField>,
  required: boolean,
) {
  if (value === undefined) return required ? 'Required.' : ''
  if (required && value.length === 0) return 'Required.'
  const length = Array.from(value).length // JSON Schema counts codepoints, not UTF-16 units.
  if (property.minLength !== undefined && length < property.minLength)
    return `Use at least ${property.minLength} characters.`
  if (property.maxLength !== undefined && length > property.maxLength)
    return `Use at most ${property.maxLength} characters.`
  if (property.pattern !== undefined) {
    try {
      if (!new RegExp(property.pattern, 'u').test(value)) return 'Use the format described below.'
    } catch {
      // Patterns unsupported by this browser are still checked by the backend.
    }
  }
  return ''
}
