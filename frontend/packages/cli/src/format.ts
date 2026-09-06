export interface FormattedOutput {
  value: unknown
  columns?: readonly string[]
}

export interface FormatContext {
  apiUrl: string
}

export type OutputFormat<Response> = (data: Response, context: FormatContext) => FormattedOutput

type ContextFreeFormat<Response> = (data: Response, context?: FormatContext) => FormattedOutput

interface ListEnvelope<Item> {
  data: Item[]
  next_cursor: string | null
}

type FieldName<Value> = Extract<keyof Value, string>

function pickFields<Value extends object, Field extends FieldName<Value>>(
  source: Value,
  fields: readonly Field[],
): Partial<Pick<Value, Field>> {
  const picked: Partial<Pick<Value, Field>> = {}
  for (const field of fields) {
    const value = source[field]
    if (value !== undefined) picked[field] = value
  }
  return picked
}

export function formatTable<Item extends object>(
  columns: readonly FieldName<Item>[],
): ContextFreeFormat<ListEnvelope<Item>> {
  return (response) => ({
    value: {
      data: response.data.map((item) => pickFields(item, columns)),
      next_cursor: response.next_cursor,
    },
    columns,
  })
}

export function formatRecord<Value extends object>(): ContextFreeFormat<Value> {
  return (data) => ({ value: data })
}

export function formatVoid(message = 'ok'): ContextFreeFormat<void> {
  return () => ({ value: message })
}
