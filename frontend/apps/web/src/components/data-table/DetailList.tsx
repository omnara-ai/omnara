import type { ReactNode } from 'react'

export interface DetailItem {
  label: string
  value: ReactNode
  /** Render the value in a monospace face (IDs, slugs, URLs). */
  mono?: boolean
}

/** Key/value grid shown inside an expanded data-table row. */
export function DetailList({ items }: { items: DetailItem[] }) {
  const visible = items.filter(
    (item) => item.value !== undefined && item.value !== null && item.value !== '',
  )
  if (visible.length === 0) {
    return <p className="text-muted-foreground text-sm">No additional details.</p>
  }
  return (
    <dl className="grid grid-cols-[max-content_minmax(0,1fr)] gap-x-8 gap-y-2 text-sm">
      {visible.map((item) => (
        <div key={item.label} className="col-span-2 grid grid-cols-subgrid">
          <dt className="text-muted-foreground">{item.label}</dt>
          <dd className={item.mono ? 'break-all font-mono text-xs leading-5' : 'break-words'}>
            {item.value}
          </dd>
        </div>
      ))}
    </dl>
  )
}
