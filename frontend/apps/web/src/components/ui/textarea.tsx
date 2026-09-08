import type { ComponentProps } from 'react'

import { cn } from '@/lib/utils'

function Textarea({ className, ...props }: ComponentProps<'textarea'>) {
  return (
    <textarea
      data-slot="textarea"
      className={cn(
        'border-input placeholder:text-muted-foreground aria-invalid:border-destructive aria-invalid:ring-destructive/20 dark:aria-invalid:ring-destructive/40 field-sizing-content pointer-coarse:text-base bg-card control-focus control-transition flex min-h-16 w-full rounded-md border px-3 py-2 text-base disabled:cursor-not-allowed disabled:opacity-50 md:text-sm',
        className,
      )}
      {...props}
    />
  )
}

export { Textarea }
