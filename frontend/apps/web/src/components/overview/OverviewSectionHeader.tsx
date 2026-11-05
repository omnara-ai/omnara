import type { ReactNode } from 'react'

/** A quiet label over an overview panel, as onboarding labels its prompt, instead of a heading. */
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
    <div className="flex flex-wrap items-baseline gap-x-3 gap-y-1 px-1">
      <h2 className="text-muted-foreground text-sm font-medium">{title}</h2>
      {subtitle && <p className="text-muted-foreground/70 text-sm">{subtitle}</p>}
      {action && <div className="ml-auto self-center">{action}</div>}
    </div>
  )
}
