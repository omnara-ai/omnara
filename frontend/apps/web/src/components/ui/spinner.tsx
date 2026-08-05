import { Loader2 } from 'lucide-react'

import { cn } from '@/lib/utils'

export function Spinner({ className }: { className?: string }) {
  return <Loader2 className={cn('h-4 w-4 animate-spin', className)} aria-hidden />
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
