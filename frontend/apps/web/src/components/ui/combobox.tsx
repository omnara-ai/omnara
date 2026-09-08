import { Combobox as ComboboxPrimitive } from '@base-ui/react/combobox'
import type { ComponentProps, ReactNode } from 'react'

import { CheckIcon, ChevronsUpDownIcon, LoaderCircleIcon, XIcon } from '@/components/icons'
import { cn } from '@/lib/utils'

const Combobox = ComboboxPrimitive.Root

function ComboboxInput({ className, ...props }: ComponentProps<typeof ComboboxPrimitive.Input>) {
  return (
    <div className="relative">
      <ComboboxPrimitive.Input
        className={cn(
          'border-input placeholder:text-muted-foreground aria-invalid:border-destructive aria-invalid:ring-destructive/20 dark:aria-invalid:ring-destructive/40 pointer-coarse:text-base control-focus control-transition bg-card h-10 w-full rounded-md border pl-3 pr-9 text-base md:text-sm',
          className,
        )}
        {...props}
      />
      <ComboboxPrimitive.Trigger className="text-muted-foreground absolute right-2 top-2.5">
        <ChevronsUpDownIcon className="size-4" />
      </ComboboxPrimitive.Trigger>
    </div>
  )
}

function ComboboxChips({ className, ...props }: ComponentProps<typeof ComboboxPrimitive.Chips>) {
  return (
    <ComboboxPrimitive.InputGroup className="w-full">
      <ComboboxPrimitive.Chips
        className={cn(
          'border-input control-focus-within control-transition bg-card flex min-h-10 w-full flex-wrap items-center gap-1 rounded-md border px-2 py-1',
          className,
        )}
        {...props}
      />
    </ComboboxPrimitive.InputGroup>
  )
}

function ComboboxChipsInput({
  className,
  ...props
}: ComponentProps<typeof ComboboxPrimitive.Input>) {
  return (
    <ComboboxPrimitive.Input
      className={cn(
        'placeholder:text-muted-foreground pointer-coarse:text-base h-6 min-w-28 flex-1 bg-transparent px-1 text-base focus-visible:outline-none md:text-sm',
        className,
      )}
      {...props}
    />
  )
}

function ComboboxChip({
  children,
  removeLabel,
  className,
  ...props
}: ComponentProps<typeof ComboboxPrimitive.Chip> & {
  children: ReactNode
  removeLabel: string
}) {
  return (
    <ComboboxPrimitive.Chip
      className={cn(
        'bg-secondary text-secondary-foreground control-focus inline-flex h-6 max-w-full items-center gap-1 rounded-sm pl-2 pr-1 text-xs font-medium',
        className,
      )}
      {...props}
    >
      <span className="truncate">{children}</span>
      <ComboboxPrimitive.ChipRemove
        aria-label={removeLabel}
        className="hover:bg-foreground/10 inline-flex size-4 shrink-0 items-center justify-center rounded-sm"
      >
        <XIcon className="size-3" />
      </ComboboxPrimitive.ChipRemove>
    </ComboboxPrimitive.Chip>
  )
}

const ComboboxValue = ComboboxPrimitive.Value

function ComboboxContent({ className, ...props }: ComponentProps<typeof ComboboxPrimitive.Popup>) {
  return (
    <ComboboxPrimitive.Portal>
      <ComboboxPrimitive.Positioner
        className="pointer-events-auto isolate z-50"
        sideOffset={4}
        align="start"
      >
        <ComboboxPrimitive.Popup
          className={cn(
            'bg-popover text-popover-foreground control-focus w-[var(--anchor-width)] min-w-64 overflow-hidden rounded-md border shadow-lg',
            className,
          )}
          // Radix modal dialogs cancel wheel/touch events that reach document
          // from outside the dialog subtree, and this popup portals to <body>.
          // Keep scroll events inside the popup so its list stays scrollable.
          onWheel={(event) => {
            event.stopPropagation()
          }}
          onTouchMove={(event) => {
            event.stopPropagation()
          }}
          {...props}
        />
      </ComboboxPrimitive.Positioner>
    </ComboboxPrimitive.Portal>
  )
}

function ComboboxList({ className, ...props }: ComponentProps<typeof ComboboxPrimitive.List>) {
  return (
    <ComboboxPrimitive.List
      className={cn('max-h-64 scroll-py-1 overflow-y-auto p-1', className)}
      {...props}
    />
  )
}

function ComboboxItem({
  className,
  children,
  ...props
}: ComponentProps<typeof ComboboxPrimitive.Item>) {
  return (
    <ComboboxPrimitive.Item
      className={cn(
        'data-[highlighted]:bg-accent data-[highlighted]:text-accent-foreground outline-hidden relative flex cursor-default items-center gap-2 rounded-sm py-2 pl-2 pr-8 text-sm data-[disabled]:pointer-events-none data-[disabled]:opacity-50',
        className,
      )}
      {...props}
    >
      {children}
      <ComboboxPrimitive.ItemIndicator className="absolute right-2">
        <CheckIcon className="size-4" />
      </ComboboxPrimitive.ItemIndicator>
    </ComboboxPrimitive.Item>
  )
}

function ComboboxEmpty({ className, ...props }: ComponentProps<typeof ComboboxPrimitive.Empty>) {
  return (
    <ComboboxPrimitive.Empty
      className={cn('text-muted-foreground px-3 py-8 text-center text-sm empty:p-0', className)}
      {...props}
    />
  )
}

function ComboboxStatus({ className, ...props }: ComponentProps<typeof ComboboxPrimitive.Status>) {
  return (
    <ComboboxPrimitive.Status
      className={cn('text-muted-foreground border-t px-3 py-2 text-xs', className)}
      {...props}
    />
  )
}

function ComboboxLoading({ label = 'Searching…' }: { label?: string }) {
  return (
    <div className="text-muted-foreground flex items-center gap-2 border-t px-3 py-2 text-xs">
      <LoaderCircleIcon className="size-3.5 motion-safe:animate-spin" />
      {label}
    </div>
  )
}

export {
  Combobox,
  ComboboxChip,
  ComboboxChips,
  ComboboxChipsInput,
  ComboboxContent,
  ComboboxEmpty,
  ComboboxInput,
  ComboboxItem,
  ComboboxList,
  ComboboxLoading,
  ComboboxStatus,
  ComboboxValue,
}
