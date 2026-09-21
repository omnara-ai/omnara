import { type ReactNode, useId } from 'react'

/** One task in the form: what it is and where to find it beside the fields that answer it. */
export function ProjectAppSetupGroup({
  title,
  hint,
  children,
}: {
  title: string
  hint: string
  children: ReactNode
}) {
  const id = useId()
  return (
    <div
      role="group"
      aria-labelledby={id}
      className="grid gap-4 border-t pt-6 text-sm first:border-t-0 first:pt-0 lg:grid-cols-[13rem_1fr] lg:gap-8"
    >
      <div className="flex flex-col gap-1.5">
        <h3 id={id} className="font-medium">
          {title}
        </h3>
        <p className="text-muted-foreground">{hint}</p>
      </div>
      <div className="flex min-w-0 flex-col gap-5">{children}</div>
    </div>
  )
}
