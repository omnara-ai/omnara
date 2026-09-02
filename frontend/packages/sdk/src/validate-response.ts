import * as z from 'zod'

import type { Config } from './generated/client'

type Issue = z.core.$ZodIssue

type ResponseValidator = NonNullable<Config['responseValidator']>

const zStringInput = z.string()

function isStringInput(issue: Issue): boolean {
  return zStringInput.safeParse(issue.input).success
}

function hasStringDiscriminator(issue: z.core.$ZodIssueInvalidUnion): boolean {
  if (issue.discriminator === undefined) return isStringInput(issue)
  return z.object({ [issue.discriminator]: zStringInput }).safeParse(issue.input).success
}

function samePath(a: readonly PropertyKey[], b: readonly PropertyKey[]): boolean {
  return a.length === b.length && a.every((segment, index) => segment === b[index])
}

function isUnknownEnumIssue(issue: Issue): boolean {
  if (issue.code === 'invalid_value') {
    return issue.values.length > 1 && isStringInput(issue)
  }
  if (issue.code !== 'invalid_union') return false
  if (issue.errors.length === 0) return hasStringDiscriminator(issue)
  if (issue.errors.some((arm) => arm.every(isUnknownEnumIssue))) {
    return true
  }
  const [first, ...rest] = issue.errors
  return (first ?? []).some(
    (candidate) =>
      candidate.code === 'invalid_value' &&
      isStringInput(candidate) &&
      rest.every((arm) =>
        arm.some((other) => other.code === 'invalid_value' && samePath(other.path, candidate.path)),
      ),
  )
}

export function isUnknownEnumError(error: z.ZodError): boolean {
  return error.issues.every(isUnknownEnumIssue)
}

export function relaxedSchema<S extends z.ZodType>(schema: S): z.ZodType<z.output<S>> {
  return z.custom<z.output<S>>((value) => {
    const result = schema.safeParse(value, { reportInput: true })
    return result.success || isUnknownEnumError(result.error)
  })
}

export function relaxedResponseValidator(schema: z.ZodType): ResponseValidator {
  return async (data) => {
    const result = await schema.safeParseAsync(data, { reportInput: true })
    if (result.success || isUnknownEnumError(result.error)) return
    throw result.error
  }
}
