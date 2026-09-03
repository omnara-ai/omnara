import { describe, expect, it } from 'vitest'

import {
  zCreateProjectMachinePoolGrantRequest,
  zProjectMachinePoolGrant,
  zUpdateProjectMachinePoolGrantRequest,
} from './generated/zod.gen'

const idleDeletionMinuteSchemas = [
  zProjectMachinePoolGrant.shape.delete_after_idle_minutes,
  zCreateProjectMachinePoolGrantRequest.shape.delete_after_idle_minutes,
  zUpdateProjectMachinePoolGrantRequest.shape.delete_after_idle_minutes,
]

describe('generated idle deletion minute contracts', () => {
  it.each([
    [0, true],
    [4, false],
    [5, true],
    [2147483648, false],
  ])('validates %d across grant schemas', (minutes, valid) => {
    for (const schema of idleDeletionMinuteSchemas) {
      expect(schema.safeParse(minutes).success).toBe(valid)
    }
  })

  it('allows null only where inheritance is explicit', () => {
    expect(zProjectMachinePoolGrant.shape.delete_after_idle_minutes.safeParse(null).success).toBe(
      true,
    )
    expect(
      zCreateProjectMachinePoolGrantRequest.shape.delete_after_idle_minutes.safeParse(null).success,
    ).toBe(false)
    expect(
      zUpdateProjectMachinePoolGrantRequest.shape.delete_after_idle_minutes.safeParse(null).success,
    ).toBe(true)
  })
})
