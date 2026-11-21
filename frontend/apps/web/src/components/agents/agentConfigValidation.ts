import { z } from 'zod'

export function configRecord<T extends z.ZodType>(valueSchema: T) {
  return z
    .custom<Record<string, z.output<T>>>(
      (value): value is object =>
        value !== null &&
        typeof value === 'object' &&
        (Object.getPrototypeOf(value) === Object.prototype ||
          Object.getPrototypeOf(value) === null),
      'Expected a mapping.',
    )
    .superRefine((value, ctx) => {
      for (const [key, item] of Object.entries(value)) {
        const parsed = valueSchema.safeParse(item)
        if (!parsed.success)
          for (const issue of parsed.error.issues)
            ctx.addIssue({ ...issue, path: [key, ...issue.path] })
      }
    })
}

type JsonValue = null | boolean | number | string | JsonValue[] | { [key: string]: JsonValue }

export const configJson: z.ZodType<JsonValue> = z.lazy(() =>
  z.union([
    z.null(),
    z.boolean(),
    z.number(),
    z.string(),
    z.array(configJson),
    configRecord(configJson),
  ]),
)
