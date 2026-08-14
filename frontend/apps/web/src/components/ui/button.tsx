import type { VariantProps } from 'class-variance-authority'
import { Slot as SlotPrimitive } from 'radix-ui'
import type { ComponentProps } from 'react'

import { buttonVariants } from '@/components/ui/button-variants'
import { cn } from '@/lib/utils'

type ButtonProps = ComponentProps<'button'> &
  VariantProps<typeof buttonVariants> & { asChild?: boolean }

function Button({ className, variant, size, asChild = false, type, ...props }: ButtonProps) {
  const classes = cn(buttonVariants({ variant, size, className }))
  if (asChild) {
    return <SlotPrimitive.Root data-slot="button" className={classes} {...props} />
  }
  return <button data-slot="button" type={type ?? 'button'} className={classes} {...props} />
}

export { Button }
export type { ButtonProps }
