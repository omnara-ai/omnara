package executionstore

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/appdefinition"
	"github.com/omnara-ai/omnara/internal/dbsafe"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
)

const (
	InteractionDestinationMaxBytes = 4 * 1024
	InteractionReceiptMaxBytes     = 16 * 1024
)

var interactionResourceKeyPattern = regexp.MustCompile(toolcatalog.ToolNamePattern)

// InteractionSelection is the agent's mutable choice for future interactions.
// The zero value means dashboard only. It is not authority to use a connection.
type InteractionSelection struct {
	IntegrationTargetID uuid.UUID `json:"integration_target_id"`
	ResourceKey         string    `json:"resource_key"`
}

// InteractionDestination is captured once, when the interaction is created.
// It retains identity only: current config and connection authority must still
// be checked before presentation or accepting a hosted provider response.
type InteractionDestination struct {
	HandlerDefinition   string                               `json:"handler_definition"`
	ResourceKey         string                               `json:"resource_key"`
	IntegrationTargetID uuid.UUID                            `json:"integration_target_id"`
	ConnectionID        uuid.UUID                            `json:"connection_id"`
	Address             integrationstore.ConversationAddress `json:"address"`
}

type InteractionDestinationOption struct {
	Destination InteractionDestination
	TargetRef   string
	DisplayName string
}

type InteractionDestinations struct {
	Current      InteractionSelection
	Destinations []InteractionDestinationOption
}

// CapturedDestination never reconstructs a missing snapshot from today's agent
// selection. Older/dashboard-only interactions have no external destination.
func (record AgentInteractionRecord) CapturedDestination() (*InteractionDestination, error) {
	if len(record.Destination) == 0 {
		return nil, nil //nolint:nilnil // A missing snapshot explicitly means dashboard-only presentation.
	}
	if err := validateInteractionObject(record.Destination, InteractionDestinationMaxBytes); err != nil {
		return nil, fmt.Errorf("interaction destination: %w", err)
	}
	var destination InteractionDestination
	decoder := json.NewDecoder(bytes.NewReader(record.Destination))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&destination); err != nil {
		return nil, fmt.Errorf("decode interaction destination: %w", err)
	}
	if err := destination.validate(); err != nil {
		return nil, err
	}
	return &destination, nil
}

func (d InteractionDestination) validate() error {
	if d.IntegrationTargetID == uuid.Nil || d.ConnectionID == uuid.Nil ||
		!interactionResourceKeyPattern.MatchString(d.ResourceKey) {
		return errors.New("interaction destination requires target, connection and resource key")
	}
	if d.HandlerDefinition == "" || len(d.HandlerDefinition) > 256 || dbsafe.Text(d.HandlerDefinition) != nil {
		return errors.New("interaction destination requires a handler definition")
	}
	return d.Address.Validate()
}

func validateInteractionObject(raw json.RawMessage, limit int) error {
	if len(raw) == 0 || len(raw) > limit || !utf8.Valid(raw) || !json.Valid(raw) || bytes.TrimSpace(raw)[0] != '{' {
		return fmt.Errorf("must be a JSON object of at most %d bytes", limit)
	}
	return dbsafe.JSONStrings(raw)
}

// Matching needs no send tool, listener, app instance or approver ACL. Canonical
// channel IDs identify the provider conversation; a broad channel may also
// authorize one of its concrete threads.
func interactionScopeContains(scope *appdefinition.Scope, address integrationstore.ConversationAddress) bool {
	if scope == nil {
		return false
	}
	kind, ref, err := scope.Conversation()
	if err != nil {
		return false
	}
	if address.Kind == kind && address.Ref == ref {
		return true
	}
	if address.Kind != "thread" {
		return false
	}
	channel, thread, found := strings.Cut(address.Ref, ":")
	if !found {
		return false
	}
	var child appdefinition.Scope
	switch {
	case scope.Slack != nil:
		child.Slack = &appdefinition.SlackScope{ChannelID: channel, ThreadTS: thread}
	case scope.Discord != nil:
		child.Discord = &appdefinition.DiscordScope{
			GuildID:   scope.Discord.GuildID,
			ChannelID: channel,
			ThreadID:  thread,
		}
	default:
		return false
	}
	return scope.Contains(child)
}

func matchingInteractionDestinations(
	resources map[string]agentconfig.AppResourceCompiled,
	targetID, connectionID uuid.UUID,
	provider string,
	address integrationstore.ConversationAddress,
) []InteractionDestination {
	var matches []InteractionDestination
	for key, resource := range resources {
		if !resource.Enabled || resource.InteractionHandler == nil || resource.Validate() != nil {
			continue
		}
		definition, found := appdefinition.Lookup(resource.Definition)
		if !found || definition.Provider != provider || !interactionScopeContains(resource.Scope, address) {
			continue
		}
		id, err := publicid.Decode(publicid.KindIntegrationConnection, resource.ConnectionID)
		if err != nil || id != connectionID {
			continue
		}
		matches = append(matches, InteractionDestination{
			HandlerDefinition: resource.InteractionHandler.Definition, ResourceKey: key,
			IntegrationTargetID: targetID, ConnectionID: connectionID, Address: address,
		})
	}
	slices.SortFunc(matches, func(a, b InteractionDestination) int {
		return strings.Compare(a.ResourceKey, b.ResourceKey)
	})
	return matches
}
