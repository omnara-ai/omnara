import type { ReactNode } from 'react'

import { SectionTitle } from '@/components/layout/SectionTitle'

/** A titled, destructive-bordered row: what the action does on the left, the trigger on the right. */
export function DangerZone({
  title,
  description,
  action,
}: {
  title: string
  description: ReactNode
  action: ReactNode
}) {
  return (
    <section className="flex flex-col gap-3">
      <SectionTitle title="Danger zone" />
      <div className="border-destructive/40 rounded-card flex flex-col gap-4 border p-5 sm:flex-row sm:items-center sm:justify-between">
        <div className="flex min-w-0 flex-col gap-1">
          <h3 className="text-sm font-medium">{title}</h3>
          <div className="text-muted-foreground text-sm">{description}</div>
        </div>
        <div className="shrink-0">{action}</div>
      </div>
    </section>
  )
}
