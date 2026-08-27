import { ApiError } from '@omnara/sdk'
import { describe, expect, it } from 'vitest'

import {
  configSubmitError,
  formatIssue,
  formatIssuePath,
  issueMarkers,
} from '@/lib/agent-config-issues'

describe('formatIssuePath', () => {
  it('renders JSON pointers with dots and indexes', () => {
    expect(formatIssuePath('')).toBe('')
    expect(formatIssuePath('/model/name')).toBe('model.name')
    expect(formatIssuePath('/machine_sources/0/max_machines')).toBe(
      'machine_sources[0].max_machines',
    )
    expect(formatIssuePath('/mcp/docs/tools/search~1all/permission')).toBe(
      'mcp.docs.tools["search/all"].permission',
    )
  })
})

describe('formatIssue', () => {
  it('omits the path for document-level issues', () => {
    expect(formatIssue({ path: '', message: 'trailing document' })).toBe('trailing document')
    expect(formatIssue({ path: '/model/name', message: 'required field is missing' })).toBe(
      'model.name: required field is missing',
    )
  })
})

describe('issueMarkers', () => {
  it('only marks issues that carry a line', () => {
    expect(
      issueMarkers([
        { path: '/model/name', message: 'required field is missing', line: 2, column: 3 },
        { path: '/bogus', message: 'unknown field', line: 7 },
        { path: '', message: 'no location' },
      ]),
    ).toEqual([
      { line: 2, column: 3, message: 'model.name: required field is missing' },
      { line: 7, column: 1, message: 'bogus: unknown field' },
    ])
  })
})

describe('configSubmitError', () => {
  it('summarises issue counts and keeps the issues', () => {
    const issues = [
      { path: '/model/name', message: 'required field is missing', line: 2 },
      { path: '/bogus', message: 'unknown field', line: 7 },
    ]
    const err = new ApiError(400, 'invalid request: agent config is invalid', 'invalid_request', {
      error: 'invalid request: agent config is invalid',
      code: 'invalid_request',
      issues,
    })
    expect(configSubmitError(err, 'Could not save')).toEqual({
      message: 'The configuration has 2 problems.',
      issues,
    })
  })

  it('falls back to the API message without issues', () => {
    const err = new ApiError(409, 'conflict: stale', 'conflict', {
      error: 'conflict: stale',
      code: 'conflict',
    })
    expect(configSubmitError(err, 'Could not save')).toEqual({
      message: 'conflict: stale',
      issues: [],
    })
    expect(configSubmitError(new Error('boom'), 'Could not save')).toEqual({
      message: 'Could not save',
      issues: [],
    })
  })
})
