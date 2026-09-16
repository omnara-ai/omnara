package channelconnector

import (
	"encoding/json"
	"time"
)

// OperationDestination is resolved by core, independently of the HTTP adapter.
// Its wire counterpart lives in the canonical OpenAPI contract; the gateway
// imports generated SDK types. Scope, request identity and deadline belong to
// the outer OperationRequest, not model arguments.
type OperationDestination struct {
	ImplementationKey string          `json:"implementation_key"`
	ProviderRef       string          `json:"provider_ref"`
	ProviderRefKind   string          `json:"provider_ref_kind"`
	ProviderMetadata  json.RawMessage `json:"provider_metadata"`
}

type ChannelGrants struct {
	Receive bool `json:"receive"`
	Read    bool `json:"read"`
	Send    bool `json:"send"`
}

type SendPayload struct {
	Destination        OperationDestination `json:"destination"`
	Message            Message              `json:"message"`
	Params             json.RawMessage      `json:"params"`
	ReplyChannelGrants *ChannelGrants       `json:"reply_channel_grants,omitempty"`
}

type MessagePublication string

const (
	MessagePublished MessagePublication = "published"
	MessageDraft     MessagePublication = "draft"
)

type MessageLocation string

const (
	MessageAtDestination  MessageLocation = "destination"
	MessageAtReplyChannel MessageLocation = "reply_channel"
)

// ReplyDestination contains facts, not authority. The operation owner pins the
// parent, connection, agent and allowed grants before sending. Only that owner
// may register these facts and expose a public channel ID.
type ReplyDestination struct {
	ImplementationKey string          `json:"implementation_key"`
	ProviderRef       string          `json:"provider_ref"`
	ProviderRefKind   string          `json:"provider_ref_kind"`
	DisplayName       string          `json:"display_name,omitempty"`
	ProviderMetadata  json.RawMessage `json:"provider_metadata,omitempty"`
}

type SendResult struct {
	Publication       MessagePublication `json:"publication"`
	MessageChannel    MessageLocation    `json:"message_channel"`
	MessageID         string             `json:"message_id,omitempty"`
	ReplyChannel      *ReplyDestination  `json:"reply_channel,omitempty"`
	CreatedAt         *time.Time         `json:"created_at,omitempty"`
	Metadata          json.RawMessage    `json:"metadata,omitempty"`
	ContinuationError *ContinuationError `json:"continuation_error,omitempty"`
}

type ReadPayload struct {
	Destination OperationDestination `json:"destination"`
	Limit       int                  `json:"limit"`
	Cursor      string               `json:"cursor,omitempty"`
}

type MessageAuthor struct {
	Ref         string `json:"ref,omitempty"`
	DisplayName string `json:"display_name,omitempty"`
}

type MessageReference struct {
	ChannelID string `json:"channel_id"`
	MessageID string `json:"message_id"`
}

type MessageObservation struct {
	Content        Message            `json:"content"`
	Publication    MessagePublication `json:"publication"`
	ChannelID      string             `json:"channel_id,omitempty"`
	MessageID      string             `json:"message_id,omitempty"`
	ReplyTo        *MessageReference  `json:"reply_to,omitempty"`
	ReplyChannelID string             `json:"reply_channel_id,omitempty"`
	Author         *MessageAuthor     `json:"author,omitempty"`
	CreatedAt      *time.Time         `json:"created_at,omitempty"`
	Metadata       json.RawMessage    `json:"metadata,omitempty"`
}

type ContinuationError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// SendMessageResult preserves a known publication even when its continuation
// cannot be registered. That failure never means the published message was unsent.
type SendMessageResult struct {
	RequestID         string             `json:"request_id"`
	Message           MessageObservation `json:"message"`
	ContinuationError *ContinuationError `json:"continuation_error,omitempty"`
}

type HistoryCoverage string

const (
	HistoryComplete HistoryCoverage = "complete"
	HistoryPartial  HistoryCoverage = "partial"
)

// ReadResult is the model-facing history page, with authorized public channel
// references and a cursor scoped to the exact project, agent and channel.
type ReadResult struct {
	Messages       []MessageObservation `json:"messages"`
	NextCursor     string               `json:"next_cursor,omitempty"`
	Coverage       HistoryCoverage      `json:"coverage"`
	CoverageReason string               `json:"coverage_reason,omitempty"`
}

// ProviderMessageReference omits Destination when the referenced message belongs
// to the requested channel. Provider addresses are facts for core to resolve,
// never authority to create or bind another channel while reading history.
type ProviderMessageReference struct {
	MessageID   string            `json:"message_id"`
	Destination *ReplyDestination `json:"destination,omitempty"`
}

type ProviderMessageObservation struct {
	Content      Message                   `json:"content"`
	Publication  MessagePublication        `json:"publication"`
	MessageID    string                    `json:"message_id,omitempty"`
	ReplyTo      *ProviderMessageReference `json:"reply_to,omitempty"`
	ReplyChannel *ReplyDestination         `json:"reply_channel,omitempty"`
	Author       *MessageAuthor            `json:"author,omitempty"`
	CreatedAt    *time.Time                `json:"created_at,omitempty"`
	Metadata     json.RawMessage           `json:"metadata,omitempty"`
}

// ProviderReadResult contains observations in the requested provider address.
// Core resolves known, authorized reply addresses and wraps pagination state;
// the transport does not need to know Omnara's channel IDs.
type ProviderReadResult struct {
	Messages       []ProviderMessageObservation `json:"messages"`
	NextCursor     string                       `json:"next_cursor,omitempty"`
	Coverage       HistoryCoverage              `json:"coverage"`
	CoverageReason string                       `json:"coverage_reason,omitempty"`
}
