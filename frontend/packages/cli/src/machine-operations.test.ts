import { describe, expect, it } from 'vitest'

import { EXEC_TIMEOUT_EXIT_CODE, execExitCode, parseEnvAssignments } from './machine-operations.ts'

describe('machine exec helpers', () => {
  it('parses KEY=VALUE assignments, keeping later = signs in the value', () => {
    expect(parseEnvAssignments([])).toBeUndefined()
    expect(parseEnvAssignments(['A=1', 'B=x=y', 'C='])).toEqual({ A: '1', B: 'x=y', C: '' })
  })

  it('exits with the remote status, or 124 on timeout', () => {
    expect(execExitCode({ exit_code: 0, timed_out: false })).toBe(0)
    expect(execExitCode({ exit_code: 7, timed_out: false })).toBe(7)
    expect(execExitCode({ exit_code: null, timed_out: true })).toBe(EXEC_TIMEOUT_EXIT_CODE)
    expect(execExitCode({ exit_code: null, timed_out: false })).toBe(1)
  })
})
