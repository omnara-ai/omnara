import { cn } from '@/lib/utils'

export function LastUsedBadge({ className }: { className?: string }) {
  return (
    <span
      className={cn(
        'bg-muted text-muted-foreground rounded px-1.5 py-0.5 text-xs leading-none font-medium',
        className,
      )}
    >
      Last used
    </span>
  )
}
