import { ApiError, type ErrorIssue } from '@omnara/sdk'

import { errorMessage } from '@/lib/submit-status'

const identifierSegment = /^[A-Za-z_][A-Za-z0-9_-]*$/
const indexSegment = /^[0-9]+$/

function pointerSegments(pointer: string): string[] {
  if (pointer === '') return []
  return pointer
    .replace(/^\//, '')
    .split('/')
    .map((segment) => segment.replaceAll('~1', '/').replaceAll('~0', '~'))
}

export function formatIssuePath(pointer: string): string {
  let path = ''
  for (const segment of pointerSegments(pointer)) {
    if (indexSegment.test(segment)) {
      path += `[${segment}]`
    } else if (identifierSegment.test(segment)) {
      path += path === '' ? segment : `.${segment}`
    } else {
      path += `[${JSON.stringify(segment)}]`
    }
  }
  return path
}

export function formatIssue(issue: ErrorIssue): string {
  const path = formatIssuePath(issue.path)
  return path === '' ? issue.message : `${path}: ${issue.message}`
}

export interface IssueMarker {
  line: number
  column: number
  message: string
}

export function issueMarkers(issues: readonly ErrorIssue[]): IssueMarker[] {
  return issues.flatMap((issue) =>
    issue.line === undefined
      ? []
      : [{ line: issue.line, column: issue.column ?? 1, message: formatIssue(issue) }],
  )
}

export interface ConfigSubmitError {
  message: string
  issues: ErrorIssue[]
}

export const noConfigError: ConfigSubmitError = { message: '', issues: [] }

export function configSubmitError(err: unknown, fallback: string): ConfigSubmitError {
  const issues = err instanceof ApiError ? err.issues : []
  if (issues.length === 0) return { message: errorMessage(err, fallback), issues }
  const count = issues.length === 1 ? '1 problem' : `${issues.length} problems`
  return { message: `The configuration has ${count}.`, issues }
}
