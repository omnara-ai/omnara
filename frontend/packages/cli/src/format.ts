import type { RenderValue } from './output.ts'

export interface FormattedOutput {
  value: RenderValue
  columns?: readonly string[]
}

export type OutputFormat<Response> = (data: Response) => FormattedOutput

interface ListEnvelope<Item> {
  data: Item[]
  next_cursor: string | null
}

type FieldName<Value> = Extract<keyof Value, string>

function pickFields<Value extends object>(
  source: Value,
  fields: readonly FieldName<Value>[],
): Record<string, RenderValue> {
  const picked: Record<string, RenderValue> = {}
  for (const field of fields) picked[field] = source[field] as RenderValue
  return picked
}

export function formatTable<Item extends object>(
  columns: readonly FieldName<Item>[],
): OutputFormat<ListEnvelope<Item>> {
  return (response) => ({
    value: {
      data: response.data.map((item) => pickFields(item, columns)),
      next_cursor: response.next_cursor,
    },
    columns,
  })
}

export function formatRecord<Value extends object>(): OutputFormat<Value> {
  return (data) => ({ value: data })
}

export function formatVoid(message = 'ok'): OutputFormat<void> {
  return () => ({ value: message })
}

export function formatFields<Value extends object>(
  fields: readonly FieldName<Value>[],
): OutputFormat<Value> {
  return (data) => ({ value: pickFields(data, fields) })
}
