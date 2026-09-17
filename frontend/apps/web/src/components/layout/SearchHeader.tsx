import type { ReactNode } from 'react'

import { SectionTitle } from '@/components/layout/SectionTitle'
import type { Guide } from '@/lib/docs'

/**
 * Header for a table or list: title on the left. Without actions the toolbar
 * (e.g. ResourceListToolbar) sits inline on the right; with actions they take
 * the right side and the toolbar moves to its own row below.
 */
export function SearchHeader({
  title,
  guide,
  toolbar,
  children,
}: {
  title: string
  guide?: Guide
  toolbar?: ReactNode
  children?: ReactNode
}) {
  const hasActions = children !== undefined && children !== null && children !== false
  return (
    <div className="flex flex-col gap-3">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div className="shrink-0 sm:mr-80">
          <SectionTitle title={title} guide={guide} />
        </div>
        {hasActions ? <div className="flex flex-wrap items-center gap-2">{children}</div> : toolbar}
      </div>
      {hasActions && toolbar && <div className="flex">{toolbar}</div>}
    </div>
  )
}
