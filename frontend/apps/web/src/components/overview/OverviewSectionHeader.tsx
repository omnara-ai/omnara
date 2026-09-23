import type { ReactNode } from 'react'

export function OverviewSectionHeader({
  title,
  subtitle,
  action,
}: {
  title: string
  subtitle?: string
  action?: ReactNode
}) {
  return (
    <div className="flex flex-wrap items-baseline gap-x-3 gap-y-1 border-b pb-3">
      <h2 className="text-2xl font-semibold tracking-[-0.02em] sm:text-[1.75rem]">{title}</h2>
      {subtitle && <p className="text-muted-foreground text-sm">{subtitle}</p>}
      {action && <div className="ml-auto self-center">{action}</div>}
    </div>
  )
}
