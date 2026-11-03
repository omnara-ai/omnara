import { describe, expect, it } from 'vitest'

import { machinePool, projectMachinePoolGrant } from '@/test/fixtures'

import { poolGrantOverrides } from './grant-override-diffs'

describe('poolGrantOverrides idle deletion', () => {
  it('omits idle deletion when the grant inherits it', () => {
    expect(poolGrantOverrides(projectMachinePoolGrant(), machinePool())).toEqual([])
  })

  it('shows an idle deletion override against the pool value', () => {
    const pool = machinePool({ delete_after_idle_minutes: 30 })
    expect(
      poolGrantOverrides(projectMachinePoolGrant({ delete_after_idle_minutes: 45 }), pool),
    ).toEqual([{ label: 'Idle deletion', value: '45 minutes', inherited: '30 minutes' }])
  })

  it('shows a zero override as disabled', () => {
    expect(
      poolGrantOverrides(projectMachinePoolGrant({ delete_after_idle_minutes: 0 }), machinePool()),
    ).toEqual([{ label: 'Idle deletion', value: 'Off', inherited: 'Off' }])
  })
})
