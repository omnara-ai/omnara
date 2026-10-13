import type { GrantOverride } from '@/components/projects/grant-override-diffs'
import { cn } from '@/lib/utils'

/** Compact "N overrides" marker shown only when a grant changes org defaults. */
export function OverrideChip({ count, className }: { count: number; className?: string }) {
  if (count === 0) return null
  return (
    <span
      className={cn(
        'border-primary/30 bg-primary/5 text-primary shrink-0 rounded-full border px-1.5 text-[11px] font-medium tabular-nums leading-4',
        className,
      )}
    >
      {count === 1 ? '1 override' : `${count} overrides`}
    </span>
  )
}

/** What the project changes, as "org default → project value" per setting. */
export function OverrideList({ overrides }: { overrides: GrantOverride[] }) {
  return (
    <dl className="grid grid-cols-[max-content_minmax(0,1fr)] gap-x-8 gap-y-2 text-sm">
      {overrides.map((override) => (
        <div key={override.label} className="col-span-2 grid grid-cols-subgrid">
          <dt className="text-muted-foreground">{override.label}</dt>
          <dd className="flex min-w-0 flex-wrap items-baseline gap-x-2 break-words">
            {override.inherited !== undefined && (
              <>
                <span className="text-muted-foreground line-through decoration-1">
                  {override.inherited}
                </span>
                <span className="text-muted-foreground" aria-label="changed to">
                  →
                </span>
              </>
            )}
            <span className="font-medium">{override.value}</span>
          </dd>
        </div>
      ))}
    </dl>
  )
}
