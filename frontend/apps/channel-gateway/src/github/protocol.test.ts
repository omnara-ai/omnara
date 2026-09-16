import { describe, expect, it } from 'vitest'

import { githubSendParamsSchema, parseGitHubParams, validateGitHubText } from './protocol'
import { oldCommit } from './test-support'

const line = {
  review_comment: true,
  commit_id: oldCommit,
  path: 'src/main.ts',
  line: 12,
  side: 'RIGHT',
}
describe('GitHub send params', () => {
  it.each([
    {},
    line,
    { ...line, review_id: 'PRR_1', start_line: 10, start_side: 'RIGHT' },
    { review_comment: true, commit_id: oldCommit, path: 'src/main.ts', subject_type: 'file' },
    { publish_review: true },
    { publish_review: true, review_id: 'PRR_1' },
  ])('preserves accepted params without rewriting %j', (params) => {
    expect(parseGitHubParams(JSON.stringify(params))).toEqual(params)
  })
  it.each([
    { ...line, publish_review: true },
    { ...line, subject_type: 'file' },
    { ...line, side: 'BOTH' },
    { ...line, line: 0 },
    { ...line, start_side: 'LEFT' },
    { review_comment: false },
    { publish_review: true, event: 'APPROVE' },
    { discard_review: true },
    { edit: 'comment' },
    { review_id: 'PRR_1' },
  ])('rejects unsupported or contradictory params %j', (params) => {
    expect(() => parseGitHubParams(JSON.stringify(params))).toThrow('invalid_params')
  })
  it('rejects duplicate keys and thread-specific authority params', () => {
    expect(() => parseGitHubParams('{"publish_review":true,"publish_review":false}')).toThrow(
      'invalid_params',
    )
    expect(() => parseGitHubParams('{"review_id":"PRR_1"}', 'review_thread')).toThrow(
      'invalid_params',
    )
    expect(parseGitHubParams('{}', 'review_thread')).toEqual({})
    expect(githubSendParamsSchema.dependentRequired).toEqual({ start_side: ['start_line'] })
  })
  it('enforces bytes and valid text without truncating', () => {
    expect(() => {
      validateGitHubText('a'.repeat(64 * 1024))
    }).not.toThrow()
    expect(() => {
      validateGitHubText('😀'.repeat(16 * 1024 + 1))
    }).toThrow('invalid_message')
    expect(() => {
      validateGitHubText('\ud800')
    }).toThrow('invalid_message')
  })
})
