import * as z from 'zod'

export async function validateResponse(schema: z.ZodType, data: unknown): Promise<void> {
  const result = await schema.safeParseAsync(data)
  if (result.success) return
  const onlyUnknownEnumValues = result.error.issues.every(
    (issue) => issue.code === 'invalid_value',
  )
  if (onlyUnknownEnumValues) return
  throw result.error
}
