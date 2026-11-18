import { cn } from '@/lib/utils'

import { formatDuration, type SpanRole, type Trace } from './otlpTrace'

const roleLabels: Record<SpanRole, string> = {
  agent: 'Turn',
  model: 'Model',
  tool: 'Tool',
  other: 'Span',
}
const roleBars: Record<SpanRole, string> = {
  agent: 'bg-muted-foreground/60',
  model: 'bg-primary',
  tool: 'bg-success',
  other: 'bg-muted-foreground/60',
}

/** The trace's spans on a shared time axis. Selecting a row highlights the span. */
export function TraceWaterfall({
  trace,
  selectedId,
  onSelect,
}: {
  trace: Trace
  selectedId: string | undefined
  onSelect: (spanId: string) => void
}) {
  const total = Math.max(trace.durationMs, 1)
  return (
    <ul aria-label="Spans" className="flex flex-col">
      {trace.spans.map((span) => {
        const selected = span.id === selectedId
        return (
          <li key={span.id}>
            <button
              type="button"
              aria-current={selected}
              className={cn(
                'control-focus grid w-full grid-cols-[minmax(0,2fr)_minmax(0,3fr)_4.5rem] items-center gap-3 rounded-md px-2 py-1.5 text-left text-sm transition-colors',
                selected ? 'bg-accent text-accent-foreground' : 'hover:bg-muted/60',
              )}
              onClick={() => {
                onSelect(span.id)
              }}
            >
              <span
                className="flex min-w-0 items-center gap-2"
                style={{ paddingLeft: `${String(span.depth)}rem` }}
              >
                <span
                  className={cn(
                    'type-fine shrink-0 rounded px-1.5 py-0.5',
                    span.error
                      ? 'bg-destructive/10 text-destructive'
                      : 'bg-muted text-muted-foreground',
                  )}
                >
                  {roleLabels[span.role]}
                </span>
                <span className="truncate" title={span.name}>
                  {span.name}
                </span>
                {span.links.length > 0 && (
                  <span className="text-muted-foreground type-fine shrink-0">
                    {span.links.length === 1 ? '1 link' : `${String(span.links.length)} links`}
                  </span>
                )}
              </span>
              <span className="bg-muted/60 relative h-2 rounded-full">
                <span
                  className={cn(
                    'absolute inset-y-0 min-w-0.5 rounded-full',
                    span.error ? 'bg-destructive' : roleBars[span.role],
                  )}
                  style={{
                    left: `${String((span.offsetMs / total) * 100)}%`,
                    width: `${String((span.durationMs / total) * 100)}%`,
                  }}
                />
              </span>
              <span className="text-muted-foreground type-code text-right tabular-nums">
                {formatDuration(span.durationMs)}
              </span>
            </button>
          </li>
        )
      })}
    </ul>
  )
}
