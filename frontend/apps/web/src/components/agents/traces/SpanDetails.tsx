import { useAgentQuery } from '@omnara/react'
import { Link } from '@tanstack/react-router'

import { parseMessages } from './genAiMessages'
import { formatDuration, type TraceLink, type TraceSpan } from './otlpTrace'
import { TraceMessages } from './TraceMessages'

function linkLabel(kind: string) {
  if (kind === 'spawned_by') return 'Spawned by'
  if (kind === 'message_from') return 'Message from'
  if (kind === 'result_from') return 'Result from'
  if (kind === 'spawned') return 'Spawned'
  if (kind === 'message_to') return 'Message to'
  return kind
}

/** Where a link leads: the other agent's Traces tab, since each turn is its own trace. */
function LinkedAgent({
  orgId,
  projectId,
  link,
}: {
  orgId: string
  projectId: string
  link: TraceLink
}) {
  const { data } = useAgentQuery(orgId, projectId, link.agentId)
  const name = data?.agent.name ?? ''
  return (
    <div className="grid min-w-0 gap-1 px-3 py-2 sm:grid-cols-[minmax(0,16rem)_minmax(0,1fr)] sm:gap-4">
      <dt className="text-muted-foreground type-caption">{linkLabel(link.kind)}</dt>
      <dd className="flex min-w-0 flex-col gap-0.5">
        <Link
          to="/projects/$projectId/agents/$agentId/traces"
          params={{ projectId, agentId: link.agentId }}
          className="type-body-small truncate hover:underline"
        >
          {name === '' ? link.agentId : name}
        </Link>
        <span className="text-muted-foreground type-code break-all">
          trace {link.traceId} · span {link.spanId}
        </span>
      </dd>
    </div>
  )
}

/** Pretty-prints values that hold JSON, such as message lists and tool arguments. */
function displayValue(value: string) {
  if (!/^\s*[[{]/.test(value)) return value
  try {
    return JSON.stringify(JSON.parse(value), null, 2)
  } catch {
    return value
  }
}

function isDocument(value: string) {
  return value.length > 80 || value.includes('\n') || /^\s*[[{]/.test(value)
}

export function SpanDetails({
  orgId,
  projectId,
  span,
}: {
  orgId: string
  projectId: string
  span: TraceSpan
}) {
  const facts = [
    { label: 'Duration', value: formatDuration(span.durationMs) },
    { label: 'Started', value: span.startedAt.toLocaleString() },
    {
      label: 'Status',
      value: span.error ? `Error${span.statusMessage && ': '}${span.statusMessage}` : 'OK',
    },
    { label: 'Span ID', value: span.id },
    ...(span.parentId ? [{ label: 'Parent span ID', value: span.parentId }] : []),
    { label: 'Trace ID', value: span.traceId },
  ]
  // Short values read as a table; messages, tool arguments and results need the full width.
  // omnara.links repeats the span's links for backends that drop them; they are listed above.
  const documents = span.attributes.filter(
    ({ key, value }) => key !== 'omnara.links' && isDocument(value),
  )
  const fields = span.attributes.filter(({ value }) => !isDocument(value))
  return (
    <div className="flex min-w-0 flex-col gap-5">
      <dl className="grid grid-cols-[auto_minmax(0,1fr)] gap-x-6 gap-y-1.5 text-sm">
        {facts.map((fact) => (
          <div key={fact.label} className="contents">
            <dt className="text-muted-foreground">{fact.label}</dt>
            <dd
              className={
                fact.label === 'Status' && span.error ? 'text-destructive break-words' : 'break-all'
              }
            >
              {fact.value}
            </dd>
          </div>
        ))}
      </dl>
      {span.links.length > 0 && (
        <div className="flex min-w-0 flex-col gap-2">
          <h3 className="type-label">Linked agents</h3>
          <dl className="bg-background flex flex-col divide-y rounded-xl border">
            {span.links.map((link) => (
              <LinkedAgent
                key={`${link.kind}:${link.traceId}:${link.spanId}`}
                orgId={orgId}
                projectId={projectId}
                link={link}
              />
            ))}
          </dl>
        </div>
      )}
      {fields.length > 0 && (
        <div className="flex min-w-0 flex-col gap-2">
          <h3 className="type-label">Attributes</h3>
          <dl className="bg-background grid gap-x-8 rounded-xl border px-3 py-2">
            {fields.map((attribute) => (
              <div
                key={attribute.key}
                className="grid min-w-0 grid-cols-[minmax(0,18rem)_minmax(0,1fr)] gap-3 py-1"
              >
                <dt className="text-muted-foreground type-code break-all">{attribute.key}</dt>
                <dd className="type-code min-w-0 break-all">{attribute.value}</dd>
              </div>
            ))}
          </dl>
        </div>
      )}
      {documents.map((attribute) => {
        const messages = parseMessages(attribute.value)
        return (
          <div key={attribute.key} className="flex min-w-0 flex-col gap-2">
            <h3 className="type-code text-muted-foreground">{attribute.key}</h3>
            {messages ? (
              <TraceMessages messages={messages} />
            ) : (
              <pre className="type-code bg-background max-h-96 overflow-auto rounded-xl border px-3 py-2 break-words whitespace-pre-wrap">
                {displayValue(attribute.value)}
              </pre>
            )}
          </div>
        )
      })}
    </div>
  )
}
