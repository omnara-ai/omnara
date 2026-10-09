import type { ReactNode } from 'react'

export function IntegrationSection({
  title,
  hideTitle = false,
  action,
  children,
}: {
  title: string
  /** Leave the heading out where a tab already names the section; it stays the region's label. */
  hideTitle?: boolean
  action?: ReactNode
  children: ReactNode
}) {
  return (
    <section aria-label={title} className="flex flex-col gap-3 text-sm">
      {(!hideTitle || action) && (
        <div className="flex min-h-9 items-center justify-between gap-3">
          {!hideTitle && <h2 className="font-medium">{title}</h2>}
          {action}
        </div>
      )}
      {children}
    </section>
  )
}
