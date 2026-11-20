import type { OtelTrace } from '@omnara/sdk'
import { z } from 'zod'

interface AnyValue {
  stringValue?: string
  intValue?: string | number
  doubleValue?: number
  boolValue?: boolean
  arrayValue?: { values?: AnyValue[] }
}

const anyValue: z.ZodType<AnyValue> = z.lazy(() =>
  z.object({
    stringValue: z.string().optional(),
    intValue: z.union([z.string(), z.number()]).optional(),
    doubleValue: z.number().optional(),
    boolValue: z.boolean().optional(),
    arrayValue: z.object({ values: z.array(anyValue).optional() }).optional(),
  }),
)

const otlpSpan = z.object({
  traceId: z.string(),
  spanId: z.string(),
  parentSpanId: z.string().optional(),
  name: z.string().default(''),
  startTimeUnixNano: z.union([z.string(), z.number()]),
  endTimeUnixNano: z.union([z.string(), z.number()]),
  attributes: z.array(z.object({ key: z.string(), value: anyValue })).default([]),
  links: z
    .array(
      z.object({
        traceId: z.string(),
        spanId: z.string(),
        attributes: z.array(z.object({ key: z.string(), value: anyValue })).default([]),
      }),
    )
    .default([]),
  status: z.object({ code: z.number().optional(), message: z.string().optional() }).optional(),
})

const otlpTrace = z.object({
  resourceSpans: z.array(
    z.object({
      scopeSpans: z.array(z.object({ spans: z.array(otlpSpan).default([]) })).default([]),
    }),
  ),
})

const statusCodeError = 2

export type SpanRole = 'agent' | 'model' | 'tool' | 'other'

/** A pointer to a span in another agent's trace: every turn is a trace of its own. */
export interface TraceLink {
  /** How the linked span relates to this one, such as spawned_by or result_from. */
  kind: string
  agentId: string
  turnId: string
  traceId: string
  spanId: string
}

export interface TraceSpan {
  id: string
  parentId: string | undefined
  traceId: string
  name: string
  role: SpanRole
  /** Nesting below the root span, which is at depth 0. */
  depth: number
  startedAt: Date
  /** Milliseconds from the start of the trace to the start of this span. */
  offsetMs: number
  durationMs: number
  error: boolean
  statusMessage: string
  attributes: { key: string; value: string }[]
  links: TraceLink[]
}

export interface Trace {
  spans: TraceSpan[]
  durationMs: number
}

function attributeText(value: AnyValue): string {
  if (value.stringValue !== undefined) return value.stringValue
  if (value.intValue !== undefined) return String(value.intValue)
  if (value.doubleValue !== undefined) return String(value.doubleValue)
  if (value.boolValue !== undefined) return String(value.boolValue)
  if (value.arrayValue) return (value.arrayValue.values ?? []).map(attributeText).join(', ')
  return ''
}

function spanRole(operation: string | undefined): SpanRole {
  if (operation === 'invoke_agent') return 'agent'
  if (operation === 'chat') return 'model'
  if (operation === 'execute_tool') return 'tool'
  return 'other'
}

/**
 * Reads an OTLP/JSON trace into spans ordered for a waterfall: each parent is followed by its
 * children, oldest first. Returns undefined when the payload is not an OTLP trace.
 */
export function parseTrace(payload: OtelTrace): Trace | undefined {
  const parsed = otlpTrace.safeParse(payload)
  if (!parsed.success) return undefined
  const raw = parsed.data.resourceSpans.flatMap((resource) =>
    resource.scopeSpans.flatMap((scope) => scope.spans),
  )
  if (raw.length === 0) return { spans: [], durationMs: 0 }

  // Nanosecond timestamps overflow a double, so they stay bigint until they are differences.
  const timed = raw.map((span) => ({
    span,
    start: BigInt(span.startTimeUnixNano),
    end: BigInt(span.endTimeUnixNano),
  }))
  const traceStart = timed.map(({ start }) => start).reduce((min, at) => (at < min ? at : min))
  const traceEnd = timed.map(({ end }) => end).reduce((max, at) => (at > max ? at : max))
  const toMs = (nanos: bigint) => Number(nanos) / 1e6

  const ids = new Set(raw.map((span) => span.spanId))
  const children = new Map<string, typeof timed>()
  for (const entry of timed) {
    const parent = entry.span.parentSpanId
    const key = parent && ids.has(parent) ? parent : ''
    children.set(key, [...(children.get(key) ?? []), entry])
  }

  const spans: TraceSpan[] = []
  function visit(parentId: string, depth: number) {
    const siblings = [...(children.get(parentId) ?? [])].sort((left, right) =>
      left.start < right.start ? -1 : left.start > right.start ? 1 : 0,
    )
    for (const { span, start, end } of siblings) {
      const attributes = span.attributes.map(({ key, value }) => ({
        key,
        value: attributeText(value),
      }))
      const operation = attributes.find(({ key }) => key === 'gen_ai.operation.name')?.value
      spans.push({
        id: span.spanId,
        parentId: span.parentSpanId,
        traceId: span.traceId,
        name: span.name,
        role: spanRole(operation),
        depth,
        startedAt: new Date(toMs(start)),
        offsetMs: toMs(start - traceStart),
        durationMs: Math.max(toMs(end - start), 0),
        error: span.status?.code === statusCodeError,
        statusMessage: span.status?.message ?? '',
        attributes,
        links: span.links.map((link) => {
          const attribute = (key: string) =>
            link.attributes.find((item) => item.key === key)?.value.stringValue ?? ''
          return {
            kind: attribute('omnara.link.kind'),
            agentId: attribute('omnara.link.agent.id'),
            turnId: attribute('omnara.link.turn.id'),
            traceId: link.traceId,
            spanId: link.spanId,
          }
        }),
      })
      visit(span.spanId, depth + 1)
    }
  }
  visit('', 0)
  return { spans, durationMs: toMs(traceEnd - traceStart) }
}

export function formatDuration(ms: number) {
  if (ms < 1) return '<1 ms'
  if (ms < 1000) return `${String(Math.round(ms))} ms`
  if (ms < 60_000) return `${(ms / 1000).toFixed(ms < 10_000 ? 2 : 1)} s`
  const minutes = Math.floor(ms / 60_000)
  return `${String(minutes)}m ${String(Math.round((ms - minutes * 60_000) / 1000))}s`
}
