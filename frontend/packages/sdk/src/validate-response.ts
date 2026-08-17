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

export async function validateResponse(schema: z.ZodType, data: unknown): Promise<void> {
  const result = await schema.safeParseAsync(data)
  if (result.success || isUnknownEnumError(result.error, data)) return
  throw result.error
}
