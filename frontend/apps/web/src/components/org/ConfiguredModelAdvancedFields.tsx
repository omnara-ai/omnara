import { ChevronDown, ChevronRight } from 'lucide-react'
import { type ReactNode } from 'react'

export function ConfiguredModelAdvancedFields({
  open,
  onToggle,
  children,
}: {
  open: boolean
  onToggle: () => void
  children: ReactNode
}) {
  return (
    <>
      <button
        type="button"
        aria-expanded={open}
        aria-controls="cm-advanced-fields"
        className="text-muted-foreground hover:text-foreground flex w-fit items-center gap-1 text-sm font-medium transition-colors"
        onClick={onToggle}
      >
        Advanced
        {open ? <ChevronDown className="size-4" /> : <ChevronRight className="size-4" />}
      </button>
      {open && (
        <div id="cm-advanced-fields" className="grid gap-3 sm:grid-cols-2">
          {children}
        </div>
      )}
    </>
  )
}
