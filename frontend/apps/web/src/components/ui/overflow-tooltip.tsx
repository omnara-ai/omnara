import { Slot } from 'radix-ui'
import { type ComponentProps, type ReactElement, useRef, useState } from 'react'

import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip'

// Radix Tooltip's data-state must not replace the wrapped control's state.
function TooltipTarget({
  'data-state': _tooltipState,
  ...props
}: ComponentProps<typeof Slot.Root> & { 'data-state'?: string }) {
  return <Slot.Root {...props} />
}

function hasClippedText(element: HTMLElement) {
  return [element, ...element.querySelectorAll('*')].some((node) => {
    const horizontalOverflow = node.scrollWidth > node.clientWidth
    const verticalOverflow = node.scrollHeight > node.clientHeight
    if (!horizontalOverflow && !verticalOverflow) return false
    const style = getComputedStyle(node)
    return (
      (horizontalOverflow && ['hidden', 'clip'].includes(style.overflowX)) ||
      (verticalOverflow && ['hidden', 'clip'].includes(style.overflowY))
    )
  })
}

export function OverflowTooltip({ children }: { children: ReactElement }) {
  const triggerRef = useRef<HTMLElement | null>(null)
  const [text, setText] = useState<string | null>(null)

  return (
    <Tooltip
      delayDuration={500}
      open={text !== null}
      onOpenChange={(open) => {
        const trigger = triggerRef.current
        setText(open && trigger && hasClippedText(trigger) ? trigger.innerText : null)
      }}
    >
      <TooltipTrigger
        asChild
        ref={(element) => {
          triggerRef.current = element
        }}
      >
        <TooltipTarget>{children}</TooltipTarget>
      </TooltipTrigger>
      <TooltipContent className="pointer-events-auto max-w-[min(28rem,calc(100vw-2rem))] whitespace-pre-wrap break-all text-left">
        {text}
      </TooltipContent>
    </Tooltip>
  )
}
