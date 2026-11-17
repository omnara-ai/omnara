import { useState } from 'react'

import { takeMcpBuilderOAuthRestore } from '@/components/agents/pendingMcpBuilderOAuth'
import { budgetAmountValid, type BudgetDraft } from '@/components/usage/budget-form'
import { canManageOrg } from '@/lib/permissions'

export type ProfileBudget = ReturnType<typeof useProfileBudget>

export function useProfileBudget(orgRole: string) {
  const [draft, setDraft] = useState<BudgetDraft>(
    () => takeMcpBuilderOAuthRestore()?.budget ?? { interval: 'month', amount: '' },
  )
  const visible = canManageOrg(orgRole)
  const amount = visible ? draft.amount.trim() : ''
  return {
    draft,
    setDraft,
    visible,
    valid: amount === '' || budgetAmountValid(amount),
    budgets: budgetAmountValid(amount) ? [{ interval: draft.interval, amount_usd: amount }] : [],
  }
}
