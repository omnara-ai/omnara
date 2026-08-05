import { cn } from '@/lib/utils'

export function LastUsedBadge({ className }: { className?: string }) {
  return (
    <span
      className={cn(
        'bg-muted text-muted-foreground rounded px-1.5 py-0.5 text-[10px] font-medium leading-none',
        className,
      )}
    >
      Last used
    </span>
  )
}
