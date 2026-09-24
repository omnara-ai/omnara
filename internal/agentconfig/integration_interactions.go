package agentconfig

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"maps"
	"slices"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/integrationdefinition"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
)

type HandlerSelection struct {
	Handler       string                      `json:"handler"`
	IntegrationID string                      `json:"integration_id"`
	Args          json.RawMessage             `json:"args"`
	Destination   integrationdefinition.Scope `json:"destination"`
}
type InteractionHandlerEntry struct {
	Handler     string          `json:"handler"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
}
type InteractionHandlerPage struct {
	Handlers   []InteractionHandlerEntry `json:"handlers"`
	Selection  *HandlerSelection         `json:"selection"`
	NextCursor string                    `json:"next_cursor,omitempty"`
}

func ListInteractionHandlers(
	handlers map[string]PreparedIntegrationInteractionHandler,
	selection *HandlerSelection,
	cursor string,
	limit int,
) (InteractionHandlerPage, error) {
	if limit == 0 {
		limit = toolcatalog.InteractionHandlersDefaultLimit
	}
	if limit < 1 || limit > toolcatalog.InteractionHandlersMaxLimit {
		return InteractionHandlerPage{}, fmt.Errorf("limit must be between 1 and %d", toolcatalog.InteractionHandlersMaxLimit)
	}
	after := ""
	if cursor != "" {
		decoded, err := base64.RawURLEncoding.DecodeString(cursor)
		if err != nil || toolcatalog.ValidateIntegrationName(string(decoded)) != nil {
			return InteractionHandlerPage{}, fmt.Errorf("invalid interaction handler cursor")
		}
		after = string(decoded)
	}
	page := InteractionHandlerPage{Handlers: []InteractionHandlerEntry{}, Selection: selection}
	keys := slices.Sorted(maps.Keys(handlers))
	for index, key := range keys {
		if key <= after {
			continue
		}
		handler := handlers[key]
		page.Handlers = append(
			page.Handlers,
			InteractionHandlerEntry{Handler: key, Description: handler.Description, InputSchema: handler.InputSchema},
		)
		if len(page.Handlers) == limit {
			if index+1 < len(keys) {
				page.NextCursor = base64.RawURLEncoding.EncodeToString([]byte(key))
			}
			break
		}
	}
	return page, nil
}

func (contract RuntimeContract) ReferencedIntegrationIDs() []uuid.UUID {
	return ReferencedIntegrationIDs(
		Compiled{Tools: contract.IntegrationTools, InteractionHandlers: contract.InteractionHandlers},
	)
}
