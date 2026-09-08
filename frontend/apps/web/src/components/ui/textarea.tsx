import type { VariantProps } from 'class-variance-authority'
import type { ComponentProps } from 'react'

import { textFieldVariants } from '@/components/ui/text-field-variants'
import { cn } from '@/lib/utils'

function Textarea({
  className,
  variant,
  ...props
}: ComponentProps<'textarea'> & VariantProps<typeof textFieldVariants>) {
  return (
    <textarea
      data-slot="textarea"
      className={cn(
        'placeholder:text-muted-foreground aria-invalid:border-destructive aria-invalid:ring-destructive/20 dark:aria-invalid:ring-destructive/40 field-sizing-content pointer-coarse:text-base control-transition flex min-h-16 w-full px-3 py-2 text-base disabled:cursor-not-allowed disabled:opacity-50 md:text-sm',
        textFieldVariants({ variant }),
        className,
      )}
      {...props}
    />
  )
}

export { Textarea }
