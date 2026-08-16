import type { VariantProps } from 'class-variance-authority'
import { Slot as SlotPrimitive } from 'radix-ui'
import type { ComponentProps, ReactNode } from 'react'

import { buttonVariants } from '@/components/ui/button-variants'
import { Spinner } from '@/components/ui/spinner'
import { cn } from '@/lib/utils'

type ButtonProps = ComponentProps<'button'> &
  VariantProps<typeof buttonVariants> & {
    asChild?: boolean
    icon?: ReactNode
    loading?: boolean
  }

function Button({
  className,
  variant,
  size,
  asChild = false,
  type,
  icon,
  loading,
  children,
  ...props
}: ButtonProps) {
  const classes = cn(buttonVariants({ variant, size, className }))
  const hasVisibleIcon = icon !== undefined || loading === true
  if (asChild) {
    return (
      <SlotPrimitive.Root data-slot="button" className={classes} {...props}>
        {children}
      </SlotPrimitive.Root>
    )
  }
  return (
    <button
      data-slot="button"
      type={type ?? 'button'}
      className={classes}
      aria-busy={loading ? true : undefined}
      data-has-icon={hasVisibleIcon ? true : undefined}
      {...props}
    >
      {loading === undefined && icon === undefined ? (
        children
      ) : (
        <>
          {/* Translation tools may rewrite label contents, so only toggle attributes on these slots. */}
          <span data-slot="button-loading-icon" aria-hidden="true" hidden={!loading}>
            <Spinner />
          </span>
          {icon !== undefined && (
            <span data-slot="button-icon" aria-hidden="true" hidden={loading}>
              {icon}
            </span>
          )}
          <span data-slot="button-label">{children}</span>
        </>
      )}
    </button>
  )
}

export { Button }
export type { ButtonProps }
