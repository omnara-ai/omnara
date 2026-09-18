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
    <section className="bg-card/70 relative rounded-xl shadow-[0_10px_32px_-20px_rgba(0,0,0,0.14)] backdrop-blur-md">
      <div
        aria-hidden="true"
        className="static-panel-ring pointer-events-none absolute inset-0 rounded-xl"
      />
      <div className="flex items-center justify-between gap-3 px-4 py-3 sm:px-5">
        <h3 className="type-label">{title}</h3>
        {action}
      </div>
      {children}
    </section>
  )
}
