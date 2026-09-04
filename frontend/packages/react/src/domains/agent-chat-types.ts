import type {
  AgentEvent,
  AgentInput,
  AgentInputKind,
  CreateAgentInputResponse,
  InlineMediaContentBlock,
  ModelOutputDelta,
  OmnaraClient,
  openAgentEventStream,
  OpenAgentEventStreamOptions,
  sdk,
} from '@omnara/sdk'
import type { QueryClient } from '@tanstack/react-query'

export interface AgentChatScope {
  orgID: string
  projectID: string
  agentID: string
}

export type AgentChatSource = 'web' | 'cli'

export interface AgentChatOptions {
  source: AgentChatSource
}

export type CreateAgentInputOptions = Parameters<typeof sdk.createAgentInput<true>>[0]

export type AgentEventStream = ReturnType<typeof openAgentEventStream>

export interface CreateAgentInputResult {
  data: CreateAgentInputResponse
}

export interface AgentChatTransport {
  openAgentEventStream: (options: OpenAgentEventStreamOptions) => AgentEventStream
  createAgentInput: (options: CreateAgentInputOptions) => Promise<CreateAgentInputResult>
}

export interface AgentChatSessionOptions extends AgentChatScope {
  client: OmnaraClient
  queryClient: QueryClient
  inputReconciliationDelayMs?: number
  transport?: AgentChatTransport
}

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
