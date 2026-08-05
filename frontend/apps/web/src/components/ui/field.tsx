import { cva, type VariantProps } from 'class-variance-authority'
import type { ComponentProps } from 'react'

import { Label } from '@/components/ui/label'
import { Separator } from '@/components/ui/separator'
import { cn } from '@/lib/utils'

function FieldGroup({ className, ...props }: ComponentProps<'div'>) {
  return (
    <div
      data-slot="field-group"
      className={cn('group/field-group flex w-full flex-col gap-6', className)}
      {...props}
    />
  )
}

const fieldVariants = cva('group/field flex w-full gap-2 data-[invalid=true]:text-destructive', {
  variants: {
    orientation: {
      vertical: 'flex-col [&>*]:w-full',
      horizontal: 'flex-row items-center',
    },
  },
  defaultVariants: { orientation: 'vertical' },
})

function Field({
  className,
  orientation = 'vertical',
  ...props
}: ComponentProps<'div'> & VariantProps<typeof fieldVariants>) {
  return (
    <div
      role="group"
      data-slot="field"
      data-orientation={orientation}
      className={cn(fieldVariants({ orientation }), className)}
      {...props}
    />
  )
}

function FieldContent({ className, ...props }: ComponentProps<'div'>) {
  return (
    <div
      data-slot="field-content"
      className={cn('flex flex-1 flex-col gap-1.5', className)}
      {...props}
    />
  )
}

function FieldLabel({ className, ...props }: ComponentProps<typeof Label>) {
  return (
    <Label
      data-slot="field-label"
      className={cn('group-data-[disabled=true]/field:opacity-50', className)}
      {...props}
    />
  )
}

function FieldDescription({ className, ...props }: ComponentProps<'p'>) {
  return (
    <p
      data-slot="field-description"
      className={cn('text-muted-foreground text-sm leading-normal', className)}
      {...props}
    />
  )
}

function FieldSeparator({ className, children, ...props }: ComponentProps<'div'>) {
  return (
    <div
      data-slot="field-separator"
      className={cn('text-muted-foreground flex items-center gap-3 text-xs', className)}
      {...props}
    >
      <Separator className="flex-1" />
      {children ? <span className="shrink-0">{children}</span> : null}
      {children ? <Separator className="flex-1" /> : null}
    </div>
  )
}

interface FieldErrorProps extends ComponentProps<'div'> {
  errors?: { message?: string }[]
}

function FieldError({ className, children, errors, ...props }: FieldErrorProps) {
  const fromErrors = errors
    ?.map((e) => e.message)
    .filter((m): m is string => Boolean(m))
    .join(', ')
  const content = children ?? fromErrors ?? null
  if (!content) {
    return null
  }
  return (
    <div
      role="alert"
      data-slot="field-error"
      className={cn('text-destructive text-sm font-medium', className)}
      {...props}
    >
      {content}
    </div>
  )
}

export { Field, FieldContent, FieldDescription, FieldError, FieldGroup, FieldLabel, FieldSeparator }
