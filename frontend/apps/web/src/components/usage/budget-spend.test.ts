import { expect, it } from 'vitest'

import { spendReachesBudget } from '@/components/usage/budget-spend'

it('compares spend with the budget exactly', () => {
  expect(spendReachesBudget('0.8', '0.8')).toBe(true)
  expect(spendReachesBudget('0.8', '0.800000000000001')).toBe(false)
  expect(spendReachesBudget('0.8', '0.79')).toBe(true)
  expect(spendReachesBudget('123456789012345', '123456789012345.000000000000001')).toBe(false)
})
