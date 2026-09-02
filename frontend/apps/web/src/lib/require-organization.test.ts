import type { CurrentUser } from '@omnara/sdk'
import { isRedirect } from '@tanstack/react-router'
import { describe, expect, it } from 'vitest'

import { requireOrganization } from '@/lib/require-organization'

function user(orgCount: number): CurrentUser {
  return {
    orgs: Array.from({ length: orgCount }, (_, index) => ({
      id: `org_${index}`,
      name: `Org ${index}`,
    })),
  } as unknown as CurrentUser
}

function redirectHref(run: () => void): string {
  try {
    run()
  } catch (error) {
    if (isRedirect(error)) return (error as { options: { href: string } }).options.href
    throw error
  }
  throw new Error('expected a redirect')
}

describe('requireOrganization', () => {
  it('lets organization members through', () => {
    expect(() => {
      requireOrganization(user(1), '/device?user_code=ABCDE-F1234')
    }).not.toThrow()
  })

  it('sends users without an organization to onboarding and preserves their destination', () => {
    expect(
      redirectHref(() => {
        requireOrganization(user(0), '/device?user_code=ABCDE-F1234')
      }),
    ).toBe('/onboarding?return_to=%2Fdevice%3Fuser_code%3DABCDE-F1234')
  })
})
