import { ApiError } from '@omnara/sdk'
import { renderToStaticMarkup } from 'react-dom/server'
import { describe, expect, it } from 'vitest'

import { InsufficientCreditsMessage } from '@/components/agents/InsufficientCreditsMessage'
import {
  isInsufficientCreditsError,
  isInsufficientCreditsModelError,
  isInsufficientCreditsToolError,
} from '@/lib/insufficient-credits'

describe('insufficient credits', () => {
  it('recognizes only the managed-work API code', () => {
    expect(
      isInsufficientCreditsError(
        new ApiError(409, 'Insufficient Omnara credits.', 'managed_work_admission_denied'),
      ),
    ).toBe(true)
    expect(isInsufficientCreditsError(new ApiError(409, 'same message', 'conflict'))).toBe(false)
  })

  it('recognizes only the managed-model error text', () => {
    expect(isInsufficientCreditsModelError('Insufficient Omnara credits.')).toBe(true)
    expect(isInsufficientCreditsModelError('The model provider request failed.')).toBe(false)
  })

  it('recognizes only the managed-work tool error code', () => {
    expect(isInsufficientCreditsToolError('managed_work_admission_denied')).toBe(true)
    expect(isInsufficientCreditsToolError('tool_work_no_progress')).toBe(false)
  })

  it('links admins to billing and directs members to an admin', () => {
    const admin = renderToStaticMarkup(
      <InsufficientCreditsMessage billingHref="https://billing.test?org=org_123" />,
    )
    const member = renderToStaticMarkup(<InsufficientCreditsMessage />)

    expect(admin).toContain('href="https://billing.test?org=org_123"')
    expect(admin).toContain('Add credits')
    expect(member).toContain('Ask an organization admin to add credits.')
    expect(member).not.toContain('<a')
  })
})
