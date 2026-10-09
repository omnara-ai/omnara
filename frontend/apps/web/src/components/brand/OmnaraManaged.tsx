import type { ReactNode } from 'react'

import { BrandMark } from '@/components/brand/OmnaraMark'

/** Text tag for a resource Omnara manages for the org (management_kind "cluster"). */
export function OmnaraManagedTag() {
  return <span className="text-muted-foreground shrink-0 text-xs">Omnara Managed</span>
}

/** Card icon content: the Omnara mark for Omnara-managed resources, else the resource's own logo. */
export function ManagedLogo({ managed, children }: { managed: boolean; children: ReactNode }) {
  return managed ? <BrandMark className="size-5" /> : children
}
