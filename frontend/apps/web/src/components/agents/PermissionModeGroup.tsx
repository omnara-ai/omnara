import type { PermissionModeOption } from '@/components/agents/permissionModeOptions'
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip'
import { cn } from '@/lib/utils'

export function PermissionModeGroup({
  label,
  options,
  value,
  disabled = false,
  className,
  onChange,
}: {
  label: string
  options: readonly PermissionModeOption[]
  value: string
  disabled?: boolean
  className?: string
  onChange: (value: string) => void
}) {
  const selected = options.find((option) => option.value === value)
  return (
    <div
      role="radiogroup"
      aria-label={label}
      className={cn('bg-muted inline-flex shrink-0 gap-0.5 rounded-lg p-1', className)}
    >
      <span className="sr-only">{selected?.label ?? value}</span>
      {options.map((option) => {
        const checked = option.value === value
        return (
          <Tooltip key={option.value}>
            <TooltipTrigger asChild>
              <button
                type="button"
                role="radio"
                aria-checked={checked}
                aria-label={option.label}
                disabled={disabled}
                className={cn(
                  'control-focus flex size-8 items-center justify-center rounded-md transition-colors disabled:pointer-events-none disabled:opacity-50 sm:size-7',
                  checked
                    ? 'bg-background text-foreground shadow-xs'
                    : 'text-muted-foreground hover:text-foreground',
                )}
                onClick={() => {
                  if (!checked) onChange(option.value)
                }}
              >
                <option.icon className="size-4.5" aria-hidden="true" />
              </button>
            </TooltipTrigger>
            <TooltipContent side="top" className="max-w-xs px-3 py-2 text-sm leading-relaxed">
              <span className="font-medium">{option.label}</span>
              {option.description && (
                <span className="text-muted-foreground block">{option.description}</span>
              )}
            </TooltipContent>
          </Tooltip>
        )
      })}
    </div>
  )
}
