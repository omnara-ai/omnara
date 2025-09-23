import { Message } from '@/types/dashboard'
import { ToolCallEnvelope } from '@/types/messages'
import { StructuredMessageCollapsible } from './StructuredMessageCollapsible'

interface ToolCallMessageProps {
  message: Message
  envelope: ToolCallEnvelope
}

function formatJson(value: unknown) {
  try {
    return JSON.stringify(value, null, 2)
  } catch (error) {
    return String(value)
  }
}

export function ToolCallMessage({ message: _message, envelope }: ToolCallMessageProps) {
  const payload = envelope?.payload

  if (!payload) {
    return null
  }

  return (
    <StructuredMessageCollapsible title={`Tool call: ${payload.tool_name}`}>
      <div className="space-y-4">
        <div className="space-y-2">
          <p className="text-xs font-semibold uppercase tracking-wide text-text-secondary">
            Input
          </p>
          <pre className="max-h-64 overflow-auto whitespace-pre-wrap rounded bg-background-base p-3 text-sm text-text-secondary">
            {formatJson(payload.input)}
          </pre>
        </div>

        {typeof payload.output !== 'undefined' && (
          <div className="space-y-2">
            <p className="text-xs font-semibold uppercase tracking-wide text-text-secondary">
              Output
            </p>
            <pre className="max-h-64 overflow-auto whitespace-pre-wrap rounded bg-background-emphasis/60 p-3 text-sm text-text-primary">
              {formatJson(payload.output)}
            </pre>
          </div>
        )}

        {payload.error && (
          <div className="space-y-2">
            <p className="text-xs font-semibold uppercase tracking-wide text-rose-300">
              Error
            </p>
            <pre className="max-h-64 overflow-auto whitespace-pre-wrap rounded bg-rose-500/10 p-3 text-sm text-rose-200">
              {payload.error}
            </pre>
          </div>
        )}
      </div>
    </StructuredMessageCollapsible>
  )
}
