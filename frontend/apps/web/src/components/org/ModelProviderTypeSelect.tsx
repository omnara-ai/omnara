import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { cn } from '@/lib/utils'

import {
  type ModelProviderOption,
  modelProviderOption,
  modelProviderOptions,
} from './CreateModelProviderDialogState'
import { ModelProviderLogo } from './ModelProviderLogo'

/** The prominent provider picker that leads the add-provider form. */
export function ModelProviderTypeSelect({
  value,
  onValueChange,
}: {
  value: ModelProviderOption
  onValueChange: (value: ModelProviderOption) => void
}) {
  const selected = modelProviderOption(value)
  return (
    <Select
      value={value}
      onValueChange={(next) => {
        const option = modelProviderOptions.find((candidate) => candidate.value === next)
        if (option) onValueChange(option.value)
      }}
    >
      <SelectTrigger
        id="mp-provider"
        aria-label="Provider"
        className="bg-muted/30 hover:border-foreground/25 data-[state=open]:border-primary data-[state=open]:ring-primary/20 w-full gap-3 rounded-xl py-2.5 pl-2.5 pr-3.5 data-[size=default]:h-auto data-[state=open]:ring-[3px] *:data-[slot=select-value]:line-clamp-none"
      >
        <SelectValue>
          <ProviderOptionContent option={selected} />
        </SelectValue>
      </SelectTrigger>
      <SelectContent>
        {modelProviderOptions.map((option) => (
          <SelectItem key={option.value} value={option.value} className="py-1.5 pl-1.5 sm:py-1.5">
            <ProviderOptionContent option={option} />
          </SelectItem>
        ))}
      </SelectContent>
    </Select>
  )
}

function ProviderOptionContent({ option }: { option: (typeof modelProviderOptions)[number] }) {
  return (
    <span className="flex min-w-0 items-center gap-3 text-left">
      <span
        className={cn(
          'bg-muted text-foreground grid size-9 shrink-0 place-items-center rounded-lg border',
          option.value === 'custom' && 'text-muted-foreground border-dashed bg-transparent',
        )}
      >
        <ModelProviderLogo provider={option.value} />
      </span>
      <span className="truncate text-sm font-medium">{option.label}</span>
    </span>
  )
}
