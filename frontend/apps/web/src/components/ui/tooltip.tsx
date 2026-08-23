import { Tooltip as TooltipPrimitive } from 'radix-ui'
import type { ComponentProps } from 'react'

import { cn } from '@/lib/utils'

function TooltipProvider({
  delayDuration = 0,
  ...props
}: ComponentProps<typeof TooltipPrimitive.Provider>) {
  return (
    <TooltipPrimitive.Provider
      data-slot="tooltip-provider"
      delayDuration={delayDuration}
      {...props}
    />
  )
}

function Tooltip({ ...props }: ComponentProps<typeof TooltipPrimitive.Root>) {
  return (
    <TooltipProvider>
      <TooltipPrimitive.Root data-slot="tooltip" {...props} />
    </TooltipProvider>
  )
}

function TooltipTrigger({ ...props }: ComponentProps<typeof TooltipPrimitive.Trigger>) {
  return <TooltipPrimitive.Trigger data-slot="tooltip-trigger" {...props} />
}

function TooltipContent({
  className,
  sideOffset = 6,
  children,
  ...props
}: ComponentProps<typeof TooltipPrimitive.Content>) {
  return (
    <TooltipPrimitive.Portal>
      <TooltipPrimitive.Content
        data-slot="tooltip-content"
        sideOffset={sideOffset}
        className={cn(
          'text-popover-foreground animate-in fade-in-0 zoom-in-95 data-[state=closed]:animate-out data-[state=closed]:fade-out-0 data-[state=closed]:zoom-out-95 data-[side=bottom]:slide-in-from-top-2 data-[side=left]:slide-in-from-right-2 data-[side=right]:slide-in-from-left-2 data-[side=top]:slide-in-from-bottom-2 origin-(--radix-tooltip-content-transform-origin) ring-sidebar-primary/25 pointer-events-none z-50 w-fit text-balance rounded-md bg-[color-mix(in_oklab,var(--sidebar-primary)_10%,var(--sidebar))] px-3 py-1.5 text-xs shadow-xl shadow-black/40 ring-1',
          className,
        )}
        {...props}
      >
        {children}
        <TooltipPrimitive.Arrow asChild width={12} height={6}>
          <svg
            aria-hidden="true"
            viewBox="0 0 30 10"
            preserveAspectRatio="none"
            className="overflow-visible"
          >
            <polygon
              points="0,0 30,0 15,10"
              className="fill-[color-mix(in_oklab,var(--sidebar-primary)_10%,var(--sidebar))]"
            />
            <rect
              x="0"
              y="-1"
              width="30"
              height="2"
              className="fill-[color-mix(in_oklab,var(--sidebar-primary)_10%,var(--sidebar))]"
            />
            <path
              d="M0 0 L15 10 L30 0"
              vectorEffect="non-scaling-stroke"
              strokeLinecap="round"
              strokeLinejoin="round"
              className="stroke-sidebar-primary/35 fill-none stroke-1"
            />
          </svg>
        </TooltipPrimitive.Arrow>
      </TooltipPrimitive.Content>
    </TooltipPrimitive.Portal>
  )
}

export { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger }
