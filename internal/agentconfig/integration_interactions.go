package agentconfig

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/storage/listing"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
)

type HandlerSelection struct {
	Handler       string          `json:"handler"`
	IntegrationID string          `json:"integration_id"`
	Args          json.RawMessage `json:"args"`
	Destination   json.RawMessage `json:"destination"`
}
type InteractionHandlerEntry struct {
	Destination json.RawMessage `json:"destination"`
	Handler     string          `json:"handler"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
}
type InteractionHandlerPage struct {
	AutoSelect bool                      `json:"auto_select"`
	Handlers   []InteractionHandlerEntry `json:"handlers"`
	Selection  *HandlerSelection         `json:"selection"`
	listing.OffsetPage
}

func ListInteractionHandlers(
	handlers map[string]InteractionHandlerEntry,
	selection *HandlerSelection,
	offset int,
	limit int,
) (InteractionHandlerPage, error) {
	if limit == 0 {
		limit = toolcatalog.InteractionHandlersDefaultLimit
	}
	if limit < 1 || limit > toolcatalog.InteractionHandlersMaxLimit {
		return InteractionHandlerPage{}, fmt.Errorf("limit must be between 1 and %d", toolcatalog.InteractionHandlersMaxLimit)
	}
	if offset < 0 {
		return InteractionHandlerPage{}, fmt.Errorf("offset must be non-negative")
	}
	page := InteractionHandlerPage{Handlers: []InteractionHandlerEntry{}, Selection: selection}
	keys := slices.Sorted(maps.Keys(handlers))
	if offset >= len(keys) {
		return page, nil
	}
	end := listing.PageEnd(offset, limit, len(keys))
	page.OffsetPage = listing.ContinueAt(end, end < len(keys))
	for _, key := range keys[offset:end] {
		handler := handlers[key]
		handler.Handler = key
		page.Handlers = append(page.Handlers, handler)
	}
	return page, nil
}

func (contract RuntimeContract) ReferencedIntegrationIDs() []uuid.UUID {
	return ReferencedIntegrationIDs(
		Compiled{
			Tools: contract.IntegrationTools, InteractionHandlers: contract.InteractionHandlers,
			GitCredentials: contract.GitCredentials,
		},
	)
}
