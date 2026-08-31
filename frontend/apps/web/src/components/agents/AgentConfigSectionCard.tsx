import type { ReactNode } from 'react'

export function AgentConfigSectionCard({
  title,
  action,
  children,
}: {
  title: string
  action: ReactNode
  children?: ReactNode
}) {
  return (
    <section className="bg-card rounded-xl border">
      <div className="flex items-center justify-between gap-3 px-4 py-3 sm:px-5">
        <h3 className="text-sm font-semibold">{title}</h3>
        {action}
      </div>
      {children ? <div className="border-t">{children}</div> : null}
    </section>
  )
}
