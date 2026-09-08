import type { ReactNode } from 'react'

/**
 * Header for a table or list: title on the left with the actions slot inline
 * on the right, then an optional toolbar row (e.g. ResourceListToolbar)
 * below. Omit the toolbar to collapse its row entirely.
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
  return (
    <div className="flex flex-col gap-3">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <h2 className="text-2xl font-bold tracking-tight">{title}</h2>
        {children && <div className="flex flex-wrap items-center gap-2">{children}</div>}
      </div>
      {toolbar}
    </div>
  )
}
