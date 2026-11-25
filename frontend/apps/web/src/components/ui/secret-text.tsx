import type { ComponentProps } from 'react'

import { cn } from '@/lib/utils'

/** A credential shown as text. Its mark keeps it out of what the assistant reads from the page. */
export function SecretText({ className, ...props }: ComponentProps<'pre'>) {
  return (
    <pre
      data-secret=""
      className={cn(
        'bg-muted min-w-0 overflow-y-auto rounded-md p-3 font-mono text-xs break-all whitespace-pre-wrap',
        className,
      )}
      {...props}
    />
  )
}
