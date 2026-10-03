import { expect, it } from 'vitest'

import { collectGrantFailures } from '@/lib/grant-failures'

const ok: PromiseSettledResult<unknown> = { status: 'fulfilled', value: undefined }
const failed: PromiseSettledResult<unknown> = {
  status: 'rejected',
  reason: new Error('Grant temporarily unavailable'),
}

it('returns null when every grant succeeded', () => {
  expect(collectGrantFailures(['proj_a', 'proj_b'], [ok, ok])).toBeNull()
})

it('names each failed project once when it failed for several resources', () => {
  expect(
    collectGrantFailures(
      ['proj_a', 'proj_b', 'proj_a', 'proj_b', 'proj_a', 'proj_b'],
      [ok, failed, ok, failed, ok, failed],
    ),
  ).toEqual({
    failedProjectIds: ['proj_b'],
    message:
      'Sharing with 1 project failed: Grant temporarily unavailable. The failed projects are still selected — retry or remove them.',
  })
})
