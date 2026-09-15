import type { ReactNode } from 'react'

/**
 * Header for a table or list: title on the left. Without actions the toolbar
 * (e.g. ResourceListToolbar) sits inline on the right; with actions they take
 * the right side and the toolbar moves to its own row below.
 */
export function SearchHeader({
  title,
  toolbar,
  children,
}: {
  title: string
  toolbar?: ReactNode
  children?: ReactNode
}) {
  const hasActions = children !== undefined && children !== null && children !== false
  return (
    <div className="flex flex-col gap-3">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <h2 className="type-title shrink-0 sm:mr-80">{title}</h2>
        {hasActions ? <div className="flex flex-wrap items-center gap-2">{children}</div> : toolbar}
      </div>
      {hasActions && toolbar && <div className="flex">{toolbar}</div>}
    </div>
  )
}
