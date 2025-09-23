export type MessageType = "TEXT" | "CODE_EDIT" | "TODO" | "TOOL_CALL" | (string & {})

export interface MessageEnvelope<
  TPayload = Record<string, unknown>,
  TType extends MessageType = MessageType
> {
  message_type: TType
  version?: number
  payload: TPayload
  // Preserve any extras such as webhook metadata or tool identifiers.
  [key: string]: unknown
}

export interface CodeEdit {
  file_path: string
  old_content?: string | null
  new_content?: string | null
  language?: string | null
}

export type CodeEditEnvelope = MessageEnvelope<
  { edits: CodeEdit[] },
  "CODE_EDIT"
>

export interface TodoItem {
  id: string
  content: string
  status: "pending" | "in_progress" | "completed"
}

export type TodoEnvelope = MessageEnvelope<
  { todos: TodoItem[] },
  "TODO"
>

export interface ToolCallPayload {
  tool_name: string
  input: Record<string, unknown>
  output?: unknown
  error?: string | null
}

export type ToolCallEnvelope = MessageEnvelope<
  ToolCallPayload,
  "TOOL_CALL"
>

export type StructuredEnvelope =
  | CodeEditEnvelope
  | TodoEnvelope
  | ToolCallEnvelope

export const STRUCTURED_MESSAGE_TYPES: readonly MessageType[] = [
  "CODE_EDIT",
  "TODO",
  "TOOL_CALL"
]

export function isStructuredEnvelope(
  value: unknown
): value is StructuredEnvelope {
  if (!value || typeof value !== "object") return false
  const envelope = value as Partial<MessageEnvelope>
  return typeof envelope.message_type === "string"
}

