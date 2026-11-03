import { Children, type ReactNode } from 'react'

import { SectionTitle } from '@/components/layout/SectionTitle'
import type { Guide } from '@/lib/docs'

/**
 * Header for a table or list: title on the left. Without actions the toolbar
 * (e.g. ResourceListToolbar) sits inline on the right; with actions they take
 * the right side and the toolbar moves to its own row below.
 */
export function SearchHeader({
  title,
  description,
  guide,
  toolbar,
  children,
}: {
  title: string
  /** One line under the title saying what the section is for. */
  description?: string
  guide?: Guide
  toolbar?: ReactNode
  children?: ReactNode
}) {
  const hasActions = Children.toArray(children).length > 0
  return (
    <div className="flex flex-col gap-3">
      <div className="flex flex-wrap items-end justify-between gap-2">
        <div className="flex min-w-0 flex-1 basis-64 flex-col gap-1">
          <SectionTitle title={title} guide={guide} />
          {description && <p className="text-muted-foreground text-sm">{description}</p>}
        </div>
        {hasActions ? (
          <div className="flex shrink-0 flex-wrap items-center gap-2">{children}</div>
        ) : (
          toolbar
        )}
      </div>
      {hasActions && toolbar && <div className="flex empty:hidden">{toolbar}</div>}
    </div>
  )
}
