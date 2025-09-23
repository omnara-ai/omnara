import { PropsWithChildren, useState } from 'react'
import { ChevronDown } from 'lucide-react'
import {
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger
} from '@/components/ui/collapsible'
import { cn } from '@/lib/utils'

interface StructuredMessageCollapsibleProps {
  title: string
  description?: string
  defaultOpen?: boolean
  className?: string
}

export function StructuredMessageCollapsible({
  title,
  description,
  defaultOpen = false,
  className,
  children
}: PropsWithChildren<StructuredMessageCollapsibleProps>) {
  const [open, setOpen] = useState(defaultOpen)

  return (
    <Collapsible
      open={open}
      onOpenChange={setOpen}
      className={cn('w-full', className)}
    >
      <CollapsibleTrigger asChild>
        <button
          type="button"
          className="w-full flex items-center justify-between rounded-lg bg-surface-panel/70 px-4 py-3 text-left transition hover:bg-surface-panel"
        >
          <div>
            <p className="font-semibold text-sm text-text-primary">{title}</p>
            {description && (
              <p className="text-xs text-text-secondary mt-1">{description}</p>
            )}
          </div>
          <ChevronDown
            className={cn(
              'h-4 w-4 text-text-secondary transition-transform',
              open ? 'rotate-180' : 'rotate-0'
            )}
          />
        </button>
      </CollapsibleTrigger>
      <CollapsibleContent className="mt-4 space-y-4">
        {children}
      </CollapsibleContent>
    </Collapsible>
  )
}

