import type { ReactNode } from 'react'

import { cn } from '@/lib/utils'

export function OverviewSectionHeader({
  title,
  subtitle,
  action,
  subtle = false,
}: {
  title: string
  subtitle?: string
  action?: ReactNode
  /** A quiet label over panels, as onboarding labels its prompt, instead of a heading. */
  subtle?: boolean
}) {
  return (
    <div className={cn('flex flex-wrap items-baseline gap-x-3 gap-y-1', subtle && 'px-1')}>
      <h2
        className={
          subtle
            ? 'text-muted-foreground text-sm font-medium'
            : 'text-2xl font-semibold tracking-[-0.02em] sm:text-[1.75rem]'
        }
      >
        {title}
      </h2>
      {subtitle && (
        <p className={cn('text-muted-foreground text-sm', subtle && 'text-muted-foreground/70')}>
          {subtitle}
        </p>
      )}
      {action && <div className="ml-auto self-center">{action}</div>}
    </div>
  )
}
