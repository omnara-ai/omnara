package httpapi

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"time"

	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/listing"
)

const (
	defaultPageLimit = 50
	maxPageLimit     = 100

	maxCursorTokenLength   = 1024
	maxCursorPayloadLength = 768
)

var errInvalidCursor = errors.New("invalid cursor")

var (
	errInvalidLimit    = errors.New("limit must be an integer between 1 and 100")
	errMalformedCursor = errors.New("cursor is malformed")
)

func parseOpenAPIPageParams(
	limitParam *openapi.PageLimit,
	cursorParam *openapi.PageCursor,
	kind publicid.Kind,
) (int, listing.KeysetCursor, error) {
	limit, err := parseOpenAPIPageLimit(limitParam)
	if err != nil {
		return 0, listing.KeysetCursor{}, err
	}
	var after listing.KeysetCursor
	if cursorParam != nil && *cursorParam != "" {
		cursor, err := decodeKeysetCursor(*cursorParam)
		if err != nil {
			return 0, listing.KeysetCursor{}, errMalformedCursor
		}
		rawID, err := parsePublicID(kind, cursor.ID)
		if err != nil {
			return 0, listing.KeysetCursor{}, errMalformedCursor
		}
		after = listing.KeysetCursor{Set: true, CreatedAt: cursor.CreatedAt, ID: rawID}
	}
	return limit, after, nil
}

func parseOpenAPIPageLimit(limitParam *openapi.PageLimit) (int, error) {
	limit := defaultPageLimit
	if limitParam != nil {
		parsed := int(*limitParam)
		if parsed < 1 || parsed > maxPageLimit {
			return 0, errInvalidLimit
		}
		limit = parsed
	}
	return limit, nil
}

func encodeNextCursor(
	hasMore bool,
	lastCreatedAt time.Time,
	kind publicid.Kind,
	lastID storage.ID,
) (cursor *string, err error) {
	if !hasMore || lastID == storage.NilID {
		return
	}
	lastPublicID, err := publicID(kind, lastID)
	if err != nil {
		return nil, err
	}
	token, err := encodeKeysetCursor(lastCreatedAt, lastPublicID)
	if err != nil {
		return nil, err
	}
	return &token, nil
}

type keysetCursor struct {
	CreatedAt time.Time `json:"created_at"`
	ID        string    `json:"id"`
}

func encodeKeysetCursor(createdAt time.Time, publicID string) (string, error) {
	payload, err := json.Marshal(keysetCursor{CreatedAt: createdAt.UTC(), ID: publicID})
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(payload), nil
}

func decodeKeysetCursor(raw string) (keysetCursor, error) {
	if len(raw) > maxCursorTokenLength {
		return keysetCursor{}, errInvalidCursor
	}
	payload, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return keysetCursor{}, errInvalidCursor
	}
	if len(payload) > maxCursorPayloadLength {
		return keysetCursor{}, errInvalidCursor
	}
	var cursor keysetCursor
	if err := json.Unmarshal(payload, &cursor); err != nil {
		return keysetCursor{}, errInvalidCursor
	}
	if cursor.CreatedAt.IsZero() || cursor.ID == "" {
		return keysetCursor{}, errInvalidCursor
	}
	return cursor, nil
}

func parseAgentInputQueuePageParams(
	limitParam *openapi.PageLimit,
	cursorParam *openapi.PageCursor,
) (int, executionstore.AgentInputQueueCursor, error) {
	limit, err := parseOpenAPIPageLimit(limitParam)
	if err != nil {
		return 0, executionstore.AgentInputQueueCursor{}, err
	}
	var after executionstore.AgentInputQueueCursor
	if cursorParam != nil && *cursorParam != "" {
		cursor, err := decodeAgentInputQueueCursor(*cursorParam)
		if err != nil {
			return 0, executionstore.AgentInputQueueCursor{}, errMalformedCursor
		}
		rawID, err := parsePublicID(publicid.KindAgentInput, cursor.ID)
		if err != nil {
			return 0, executionstore.AgentInputQueueCursor{}, errMalformedCursor
		}
		after = executionstore.AgentInputQueueCursor{
			Set:          true,
			DeliveryMode: executionstore.AgentInputDeliveryMode(cursor.DeliveryMode),
			InputRank:    cursor.InputRank,
			QueuedAt:     cursor.QueuedAt,
			ID:           rawID,
		}
	}
	return limit, after, nil
}

func encodeNextAgentInputQueueCursor(hasMore bool, last executionstore.AgentInputRecord) (cursor *string, err error) {
	if !hasMore || last.ID == storage.NilID {
		return
	}
	lastPublicID, err := publicID(publicid.KindAgentInput, last.ID)
	if err != nil {
		return nil, err
	}
	token, err := encodeAgentInputQueueCursor(
		agentInputQueueCursor{
			DeliveryMode: string(last.DeliveryMode),
			InputRank:    last.InputRank,
			QueuedAt:     last.QueuedAt,
			ID:           lastPublicID,
		},
	)
	if err != nil {
		return nil, err
	}
	return &token, nil
}

type agentInputQueueCursor struct {
	DeliveryMode string    `json:"delivery_mode"`
	InputRank    int64     `json:"input_rank"`
	QueuedAt     time.Time `json:"queued_at"`
	ID           string    `json:"id"`
}

func encodeAgentInputQueueCursor(cursor agentInputQueueCursor) (string, error) {
	payload, err := json.Marshal(
		agentInputQueueCursor{
			DeliveryMode: cursor.DeliveryMode,
			InputRank:    cursor.InputRank,
			QueuedAt:     cursor.QueuedAt.UTC(),
			ID:           cursor.ID,
		},
	)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(payload), nil
}

func decodeAgentInputQueueCursor(raw string) (agentInputQueueCursor, error) {
	if len(raw) > maxCursorTokenLength {
		return agentInputQueueCursor{}, errInvalidCursor
	}
	payload, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return agentInputQueueCursor{}, errInvalidCursor
	}
	if len(payload) > maxCursorPayloadLength {
		return agentInputQueueCursor{}, errInvalidCursor
	}
	var cursor agentInputQueueCursor
	if err := json.Unmarshal(payload, &cursor); err != nil {
		return agentInputQueueCursor{}, errInvalidCursor
	}
	if cursor.DeliveryMode == "" {
		cursor.DeliveryMode = string(executionstore.DeliveryModeQueued)
	}
	if (cursor.DeliveryMode != string(executionstore.DeliveryModeSteering) &&
		cursor.DeliveryMode != string(executionstore.DeliveryModeQueued)) ||
		cursor.InputRank <= 0 || cursor.QueuedAt.IsZero() || cursor.ID == "" {
		return agentInputQueueCursor{}, errInvalidCursor
	}
	return cursor, nil
}
