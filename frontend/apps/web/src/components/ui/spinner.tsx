import { cn } from '@/lib/utils'

export function Spinner({ className }: { className?: string }) {
  return (
    <span
      className={cn(
        'state-loading inline-block size-5 shrink-0 motion-safe:animate-spin',
        className,
      )}
      aria-hidden
    />
  )
}

export function FullPageSpinner() {
  return (
    <div
      className="flex h-full w-full items-center justify-center"
      role="status"
      aria-live="polite"
    >
      <Spinner className="text-muted-foreground h-6 w-6" />
      <span className="sr-only">Loading</span>
    </div>
  )
}
