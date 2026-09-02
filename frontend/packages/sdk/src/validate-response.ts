import type * as z from 'zod'

type Issue = z.core.$ZodIssue

function valueAt(data: unknown, path: readonly PropertyKey[]): unknown {
  let current: unknown = data
  for (const segment of path) {
    if (current === null || typeof current !== 'object') return undefined
    current = (current as Record<PropertyKey, unknown>)[segment]
  }
  return current
}

function samePath(a: readonly PropertyKey[], b: readonly PropertyKey[]): boolean {
  return a.length === b.length && a.every((segment, index) => segment === b[index])
}

function isUnknownEnumIssue(issue: Issue, root: unknown): boolean {
  if (issue.code === 'invalid_value') {
    return issue.values.length > 1 && typeof valueAt(root, issue.path) === 'string'
  }
  if (issue.code !== 'invalid_union') return false
  const input = valueAt(root, issue.path)
  if (issue.errors.length === 0) return typeof input === 'string'
  if (issue.errors.some((arm) => arm.every((member) => isUnknownEnumIssue(member, input)))) {
    return true
  }
  const [first, ...rest] = issue.errors
  return (first ?? []).some(
    (candidate) =>
      candidate.code === 'invalid_value' &&
      typeof valueAt(input, candidate.path) === 'string' &&
      rest.every((arm) =>
        arm.some((other) => other.code === 'invalid_value' && samePath(other.path, candidate.path)),
      ),
  )
}

export function isUnknownEnumError(error: z.ZodError, data: unknown): boolean {
  return error.issues.every((issue) => isUnknownEnumIssue(issue, data))
}

type ParsedResponse<T> = { data: T; error?: undefined } | { data?: undefined; error: z.ZodError }

// Accepts unknown enum members and keeps unknown fields so newer servers keep
// working; anything else must match. Returns the original value, typed.
export function parseResponse<T extends z.ZodType>(
  schema: T,
  data: unknown,
): ParsedResponse<z.output<T>> {
  const result = schema.safeParse(data)
  if (result.success || isUnknownEnumError(result.error, data)) {
    return { data: data as z.output<T> }
  }
  return { error: result.error }
}

export function validateResponse(schema: z.ZodType, data: unknown): Promise<void> {
  const { error } = parseResponse(schema, data)
  return error == null ? Promise.resolve() : Promise.reject(error)
}
