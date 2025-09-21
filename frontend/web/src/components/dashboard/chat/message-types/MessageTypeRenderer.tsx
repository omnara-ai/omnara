import { ReactNode } from 'react'
import { Message } from '@/types/dashboard'
import {
  CodeEditEnvelope,
  MessageEnvelope,
  MessageType,
  TodoEnvelope,
  ToolCallEnvelope
} from '@/types/messages'
import { CodeEditMessage } from './CodeEditMessage'
import { TodoMessage } from './TodoMessage'
import { ToolCallMessage } from './ToolCallMessage'

function coerceEnvelope(metadata: Message['message_metadata']): MessageEnvelope | undefined {
  if (!metadata || typeof metadata !== 'object') {
    return undefined
  }

  const candidate = metadata as Partial<MessageEnvelope> & Record<string, unknown>

  if (typeof candidate.message_type === 'string') {
    return candidate as MessageEnvelope
  }

  if (typeof candidate.kind === 'string') {
    return {
      ...candidate,
      message_type: candidate.kind as MessageType
    } as MessageEnvelope
  }

  return undefined
}

export interface MessageTypeRendererProps {
  message: Message
  renderDefault: () => ReactNode
}

export function MessageTypeRenderer({
  message,
  renderDefault
}: MessageTypeRendererProps) {
  const envelope = coerceEnvelope(message.message_metadata)
  const resolvedType: MessageType | undefined =
    message.message_type ?? envelope?.message_type

  if (!resolvedType || !envelope) {
    return <>{renderDefault()}</>
  }

  switch (resolvedType) {
    case 'CODE_EDIT':
      return (
        <div className="space-y-4">
          {renderDefault()}
          <CodeEditMessage
            message={message}
            envelope={envelope as CodeEditEnvelope}
          />
        </div>
      )
    case 'TODO':
      return (
        <div className="space-y-4">
          {renderDefault()}
          <TodoMessage
            message={message}
            envelope={envelope as TodoEnvelope}
          />
        </div>
      )
    case 'TOOL_CALL':
      return (
        <div className="space-y-4">
          {renderDefault()}
          <ToolCallMessage
            message={message}
            envelope={envelope as ToolCallEnvelope}
          />
        </div>
      )
    default:
      return <>{renderDefault()}</>
  }
}
