import { describe, expect, it } from 'vitest'

import {
  zCreateProjectMachinePoolGrantRequest,
  zProjectMachinePoolGrant,
  zUpdateProjectMachinePoolGrantRequest,
} from './generated/zod.gen'

const grant = zProjectMachinePoolGrant.pick({ delete_after_idle_minutes: true })
const create = zCreateProjectMachinePoolGrantRequest.pick({ delete_after_idle_minutes: true })
const update = zUpdateProjectMachinePoolGrantRequest.pick({ delete_after_idle_minutes: true })
const idleDeletionMinuteSchemas = [grant, create, update]

describe('generated idle deletion minute contracts', () => {
  it.each([
    [0, true],
    [4, false],
    [5, true],
    [2147483648, false],
  ])('validates %d across grant schemas', (minutes, valid) => {
    for (const schema of idleDeletionMinuteSchemas) {
      expect(schema.safeParse({ delete_after_idle_minutes: minutes }).success).toBe(valid)
    }
  })

  it('allows null only where inheritance is explicit', () => {
    expect(grant.safeParse({ delete_after_idle_minutes: null }).success).toBe(true)
    expect(create.safeParse({ delete_after_idle_minutes: null }).success).toBe(false)
    expect(update.safeParse({ delete_after_idle_minutes: null }).success).toBe(true)
  })
})
