import type { VariantProps } from 'class-variance-authority'
import type { ComponentProps } from 'react'

import { textFieldVariants } from '@/components/ui/text-field-variants'
import { cn } from '@/lib/utils'

function Input({
  className,
  type,
  variant,
  ...props
}: ComponentProps<'input'> & VariantProps<typeof textFieldVariants>) {
  return (
    <input
      type={type}
      data-slot="input"
      className={cn(
        'file:text-foreground placeholder:text-muted-foreground selection:bg-primary selection:text-primary-foreground pointer-coarse:text-base control-transition flex h-10 w-full min-w-0 px-3 py-1 text-base file:inline-flex file:h-7 file:border-0 file:bg-transparent file:text-sm file:font-medium disabled:pointer-events-none disabled:cursor-not-allowed disabled:opacity-50 md:text-sm',
        'aria-invalid:border-destructive aria-invalid:ring-destructive/20 dark:aria-invalid:ring-destructive/40',
        textFieldVariants({ variant }),
        className,
      )}
      {...props}
    />
  )
}

export { Input }
