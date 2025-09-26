# Message Types Implementation Plan

## Overview
This document outlines the implementation of a rich message type system for Omnara's messaging platform. The system will support multiple message types (TEXT, CODE_EDIT, TODO, TOOL_CALL) using the existing `message_metadata` JSONB field, requiring no database migrations.

## Status Snapshot *(updated 2024-05-30)*
- [x] Phase 1 – Backend foundation and API surfaces normalized metadata end-to-end
- [x] Phase 2.1 – SDK models hydrate structured metadata and queued messages
- [x] Phase 2.2 – Added `send_structured_message` plus thin convenience wrappers for CODE_EDIT/TODO/TOOL_CALL
- [ ] Phase 2.3 – Async SDK parity cleanup (in progress)
- [ ] Phase 2.4 – CLI wrapper metadata wiring
- [ ] Phase 3 – Frontend rendering work *(message component router implementation in progress)*

## Architecture Decisions

### Storage Strategy
- **Use existing `message_metadata` JSONB field** – No database migration needed
- **Structured envelope** – metadata is stored as `{ "message_type": "TYPE", "version": 1, "payload": {...}, ...extras }`
- **Extras stay at envelope level** – existing keys such as `webhook_url` remain alongside the envelope fields
- **Main `content` field** contains human-readable summary/preview
- **Backward compatibility** – null/missing metadata (or non-envelope dicts) are treated as legacy TEXT messages

### Message Type Schemas
Structured messages share the same envelope, so the payload changes by message type.

```json
{
  "message_type": "CODE_EDIT",
  "version": 1,
  "payload": {
    "edits": [
      {
        "file_path": "src/main.py",
        "old_content": "print('old')\n",
        "new_content": "print('new')\n",
        "language": "python"
      }
    ]
  },
  "webhook_url": "https://example.com/hook"  // optional extras still live here
}
```

Payload models (all validated through Pydantic):

| Message Type | Payload shape | Notes |
| ------------ | ------------- | ----- |
| `TEXT` | _(no structured payload)_ | plain markdown stays in `content`; envelope optional |
| `CODE_EDIT` | `{"edits": [{"file_path": str, "old_content": str\|None, "new_content": str\|None, "language": str\|None}]}` | supports create/edit/delete via `None` values |
| `TODO` | `{"todos": [{"id": str, "content": str, "status": "pending"\|"in_progress"\|"completed"}]}` | mirrors TodoWrite tool |
| `TOOL_CALL` | `{"tool_name": str, "input": dict, "output": Any | None, "error": str | None}` | captures tool invocation plus result |

## Implementation Phases

### Phase 1: Backend Foundation

#### 1.1 Create Message Type Schemas (`shared/message_types.py`)
- Envelope model with `message_type`, `version`, and `payload`
- Payload models for CODE_EDIT, TODO, TOOL_CALL (`TEXT` uses plain content)
- Helpers:
  - `normalize_message_metadata` – validate + sanitize data before persistence
  - `parse_message_metadata` – parse envelopes when returning responses
  - `ParsedMessageMetadata` dataclass – common container for parsed results
- Unknown message types are left untouched so third-party agents can experiment without waiting for backend updates

#### 1.2 Update Message Creation (`servers/shared/db/queries.py`)
- Validate metadata on every agent message write (ValueError -> 400 to clients)
- Preserve legacy dictionaries without a `message_type`
- Store normalized envelopes so frontend receives consistent structures

#### 1.2 Update Message Creation (`servers/shared/db/queries.py`)
- Add `message_metadata` parameter to `send_agent_message`
- Validate metadata based on type
- Generate appropriate content preview for non-TEXT types

#### 1.3 Update API Models (`servers/fastapi_server/models.py`)
- Request payload remains unchanged (`content` + optional `message_metadata`)
- `MessageResponse` now returns normalized metadata plus a shortcut `message_type`
- Router helper `_serialize_message` centralizes conversion logic so every endpoint stays consistent

### Phase 2: SDK Implementation

#### 2.1 Update SDK Models (`omnara/sdk/models.py`)
- Expand `Message` to include `message_type`, `message_metadata`, and typed payload accessors
- Update `CreateMessageResponse.queued_user_messages` to carry structured `Message` objects
- Ensure dataclasses remain backwards-compatible with optional fields

#### 2.2 Add Helper Methods (`omnara/sdk/client.py`)
- Introduce a unified `send_structured_message(message_type, payload, *, extras, version, **kwargs)` helper (thin wrappers remain for convenience)
- Allow `send_message` to accept an optional `message_metadata` envelope for advanced callers
- Parse responses with `parse_message_metadata` before instantiating `Message`

#### 2.3 Async SDK Parity (`omnara/sdk/async_client.py`)
- Mirror the sync helpers and response parsing
- Apply the same optional metadata and envelope handling to async flows
- Share serialization utilities to avoid drift

#### 2.4 CLI Wrapper Integration (`integrations/cli_wrappers/claude_code/format_utils.py`)
- Convert tool events into structured envelopes (e.g., `CODE_EDIT`, `TOOL_CALL`)
- Return both markdown preview text and metadata so callers pass structured data to SDK helpers
- Maintain terminal-friendly formatting for legacy display paths

### Phase 3: Frontend Implementation

#### 3.1 Type Definitions (`frontend/web/src/types/messages.ts`)
```typescript
export type MessageType = "TEXT" | "CODE_EDIT" | "TODO" | "TOOL_CALL" | string;

export interface CodeEdit {
  file_path: string;
  old_content?: string | null;
  new_content?: string | null;
  language?: string | null;
}

export interface TodoItem {
  id: string;
  content: string;
  status: "pending" | "in_progress" | "completed";
}

export interface MessageEnvelope<TPayload = Record<string, unknown>, TType extends MessageType = MessageType> {
  message_type: TType;
  version?: number;
  payload: TPayload;
  // extras such as webhook_url remain optional fields on the object
  [key: string]: unknown;
}

export type CodeEditEnvelope = MessageEnvelope<{ edits: CodeEdit[] }, "CODE_EDIT">;
export type TodoEnvelope = MessageEnvelope<{ todos: TodoItem[] }, "TODO">;
export type ToolCallEnvelope = MessageEnvelope<
  { tool_name: string; input: Record<string, unknown>; output?: unknown; error?: string },
  "TOOL_CALL"
>;
```

#### 3.2 Message Router Component (`frontend/web/src/components/dashboard/chat/MessageRenderer.tsx`)
```tsx
import { TextMessage } from './message-types/TextMessage';
import { CodeEditMessage } from './message-types/CodeEditMessage';
import { TodoMessage } from './message-types/TodoMessage';
import { ToolCallMessage } from './message-types/ToolCallMessage';

export function MessageRenderer({ message }) {
  const messageType = message.message_type ?? message.message_metadata?.message_type ?? 'TEXT';

  switch (messageType) {
    case 'CODE_EDIT':
      return <CodeEditMessage envelope={message.message_metadata} />;
    case 'TODO':
      return <TodoMessage envelope={message.message_metadata} />;
    case 'TOOL_CALL':
      return <ToolCallMessage envelope={message.message_metadata} />;
    default:
      return <TextMessage content={message.content} />;
  }
}
```

#### 3.3 Structured Message Components (`frontend/web/src/components/dashboard/chat/message-types/`)
- Create dedicated components for `CODE_EDIT`, `TODO`, and `TOOL_CALL` that render collapsible detail views using shared UI primitives
- Provide a `MessageTypeRenderer` wrapper that selects the appropriate component (falls back to legacy markdown renderer for TEXT/unknown)
- Ensure components are easy to extend by exporting a registry map keyed by `message_type`

#### 3.3 Component Examples

**CodeEditMessage.tsx**:
- Render diffs with syntax highlighting
- Show file paths
- Red/green highlighting for changes
- Collapsible for large edits

**TodoMessage.tsx**:
- Interactive checkboxes
- Status indicators (○ pending, ◐ in_progress, ● completed)
- Strike-through for completed items

**ToolCallMessage.tsx**:
- Collapsible view
- Show tool name prominently
- Format input/output as JSON
- Error state handling

### Phase 4: Migration & Testing

#### 4.1 Backward Compatibility
- All existing messages work without changes (treated as TEXT)
- No database migration required
- Frontend gracefully handles unknown types

#### 4.2 Testing Strategy
1. **Unit Tests**: Message type validation
2. **Integration Tests**: End-to-end message flow
3. **UI Tests**: Component rendering for each type
4. **Compatibility Tests**: Legacy message handling

## Success Metrics
- ✅ Zero downtime deployment
- ✅ No data migration required
- ✅ Rich message rendering in frontend
- ✅ Type-safe message creation in SDK
- ✅ Extensible for future message types

## Future Enhancements
- **APPROVAL**: Multiple choice questions with options
- **FILE**: File attachments with previews
- **CHART**: Data visualizations
- **CODE_REVIEW**: Line-by-line code feedback
- **PROGRESS**: Progress bars with percentages
- **ERROR**: Structured error messages with stack traces

## Implementation Timeline
1. **Week 1**: Backend schemas and API updates
2. **Week 2**: SDK implementation and testing
3. **Week 3**: Frontend components
4. **Week 4**: Integration testing and deployment

## Rollback Plan
If issues arise:
1. Frontend falls back to TEXT rendering for all messages
2. Backend continues accepting messages without type validation
3. No database changes to revert
4. Can disable feature with feature flag
