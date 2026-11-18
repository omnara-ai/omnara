import type { TraceMessage } from './genAiMessages'

function Part({ part }: { part: TraceMessage['parts'][number] }) {
  if (part.type === 'text')
    return <p className="text-sm break-words whitespace-pre-wrap">{part.content}</p>
  if (part.type === 'tool_call')
    return (
      <p className="type-code break-words">
        {part.name} <span className="text-muted-foreground">{part.arguments}</span>
      </p>
    )
  return <pre className="type-code break-words whitespace-pre-wrap">{part.json}</pre>
}

/** Messages as a conversation: who spoke, then the text and tool calls they produced. */
export function TraceMessages({ messages }: { messages: TraceMessage[] }) {
  return (
    <ol className="bg-background flex max-h-96 flex-col divide-y overflow-y-auto rounded-xl border">
      {messages.map((message, index) => (
        // Messages have no identity of their own; their order is fixed.
        // eslint-disable-next-line react/no-array-index-key
        <li key={index} className="flex min-w-0 flex-col gap-2 px-3 py-2">
          <span className="text-muted-foreground type-caption capitalize">{message.role}</span>
          {message.parts.map((part, partIndex) => (
            // eslint-disable-next-line react/no-array-index-key
            <Part key={partIndex} part={part} />
          ))}
        </li>
      ))}
    </ol>
  )
}
