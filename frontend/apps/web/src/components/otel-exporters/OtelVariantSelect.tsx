import type { OtelTraceVariant } from '@omnara/sdk'

import { otelVariantLabels, variants } from '@/components/otel-exporters/otelExporterOptions'
import { OtelVariantLogo } from '@/components/otel-exporters/OtelVariantLogo'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'

/** The prominent trace-variant picker that leads the exporter form, like the provider picker. */
export function OtelVariantSelect({
  value,
  onValueChange,
}: {
  value: OtelTraceVariant
  onValueChange: (value: OtelTraceVariant) => void
}) {
  return (
    <Select
      value={value}
      onValueChange={(next) => {
        const variant = variants.find((candidate) => candidate === next)
        if (variant) onValueChange(variant)
      }}
    >
      <SelectTrigger
        aria-label="Trace Variant"
        className="bg-muted/30 hover:border-foreground/25 data-[state=open]:border-primary data-[state=open]:ring-primary/20 w-full gap-3 rounded-xl py-2.5 pr-3.5 pl-2.5 data-[size=default]:h-auto *:data-[slot=select-value]:line-clamp-none data-[state=open]:ring-[3px]"
      >
        <SelectValue>
          <VariantOption variant={value} />
        </SelectValue>
      </SelectTrigger>
      <SelectContent>
        {variants.map((variant) => (
          <SelectItem key={variant} value={variant} className="py-1.5 pl-1.5 sm:py-1.5">
            <VariantOption variant={variant} />
          </SelectItem>
        ))}
      </SelectContent>
    </Select>
  )
}

function VariantOption({ variant }: { variant: OtelTraceVariant }) {
  return (
    <span className="flex min-w-0 items-center gap-3 text-left">
      <span className="bg-muted text-foreground grid size-9 shrink-0 place-items-center rounded-lg border">
        <OtelVariantLogo variant={variant} />
      </span>
      <span className="truncate text-sm font-medium">{otelVariantLabels[variant]}</span>
    </span>
  )
}
