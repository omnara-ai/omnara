import type {
  AgentEvent,
  AgentInput,
  AgentInputKind,
  InlineMediaContentBlock,
  ModelOutputDelta,
} from '@omnara/sdk'

export interface AgentChatAttachmentInput {
  data: string
  mediaType: InlineMediaContentBlock['media_type']
  filename: string
  sizeBytes: number
}

export interface AgentChatMessageInput {
  text: string
  attachments?: AgentChatAttachmentInput[]
}

export interface OmnaraMessageMetadata {
  eventId?: string
  eventKind?: string
  inputKind?: AgentInputKind
  actorId?: string
  sequence?: number
  turnId?: string
  turnSequence?: number
  createdAt?: string
}

export type AgentChatStatus = 'submitted' | 'streaming' | 'ready' | 'error'

export interface LocalAgentInput {
  id: string
  text: string
  attachments?: AgentChatAttachmentInput[]
  attachmentCount?: number
  placement: 'conversation' | 'backlog'
  agentInputID?: string
}

export interface AgentChatData {
  events: AgentEvent[]
  deltas: ModelOutputDelta[]
  localInputs: LocalAgentInput[]
  backlogInputs: AgentInput[]
  error: Error | undefined
  hasOlderEvents: boolean
}
