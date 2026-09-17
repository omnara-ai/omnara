package channelconnector

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/omnara-ai/omnara/internal/dbsafe"
	"github.com/omnara-ai/omnara/internal/jsoncanonical"
	"github.com/omnara-ai/omnara/internal/registryname"
)

func DecodeSendResult(raw json.RawMessage) (SendResult, error) {
	var result SendResult
	if _, err := decodeMessageResult(raw, &result); err != nil {
		return SendResult{}, err
	}
	if !validPublication(result.Publication) ||
		(result.MessageChannel != MessageAtDestination && result.MessageChannel != MessageAtReplyChannel) ||
		(result.MessageChannel == MessageAtReplyChannel && result.ReplyChannel == nil) {
		return SendResult{}, errors.New("send result requires a publication state and an actual message location")
	}
	if err := boundedResultText(result.MessageID, 2048); err != nil {
		return SendResult{}, fmt.Errorf("send result message ID: %w", err)
	}
	metadata, err := NormalizeOpaqueObject(result.Metadata)
	if err != nil {
		return SendResult{}, fmt.Errorf("send result metadata: %w", err)
	}
	result.Metadata = metadata
	if err := validateReplyDestination(result.ReplyChannel); err != nil {
		return SendResult{}, err
	}
	if failure := result.ContinuationError; failure != nil {
		if result.ReplyChannel != nil || result.MessageChannel != MessageAtDestination ||
			!registryname.Valid(failure.Code) || strings.TrimSpace(failure.Message) == "" ||
			boundedResultText(failure.Message, 1024) != nil {
			return SendResult{}, errors.New("continuation failure requires a known message without an available reply channel")
		}
	}
	return result, nil
}

// DecodeReadResult checks provider facts and page bounds. The caller supplies
// the canonical channel from the operation, authorizes artifacts, and resolves
// only existing authorized reply addresses before exposing the page.
func DecodeReadResult(raw json.RawMessage, limit int) (ProviderReadResult, error) {
	if limit < 1 || limit > 100 {
		return ProviderReadResult{}, errors.New("invalid history request scope or limit")
	}
	var result ProviderReadResult
	fields, err := decodeMessageResult(raw, &result)
	if err != nil {
		return ProviderReadResult{}, err
	}
	if result.Messages == nil || len(result.Messages) > limit ||
		(result.Coverage != HistoryComplete && result.Coverage != HistoryPartial) {
		return ProviderReadResult{}, errors.New("history result requires bounded messages and explicit coverage")
	}
	if result.Coverage == HistoryPartial && result.CoverageReason == "" {
		return ProviderReadResult{}, errors.New("partial history requires an explanation")
	}
	if err := boundedResultText(result.NextCursor, 4096); err != nil {
		return ProviderReadResult{}, fmt.Errorf("history cursor: %w", err)
	}
	if err := boundedResultText(result.CoverageReason, 1024); err != nil {
		return ProviderReadResult{}, fmt.Errorf("history coverage: %w", err)
	}
	observations, ok := fields["messages"].([]any)
	if !ok || len(observations) != len(result.Messages) {
		return ProviderReadResult{}, errors.New("history messages must be an array")
	}
	for i := range result.Messages {
		message := &result.Messages[i]
		observation, ok := observations[i].(map[string]any)
		if !ok {
			return ProviderReadResult{}, errors.New("history message must be an object")
		}
		if _, ok := observation["content"].(map[string]any); !ok {
			return ProviderReadResult{}, errors.New("history message content must be an object")
		}
		if !validPublication(message.Publication) {
			return ProviderReadResult{}, errors.New("invalid history message publication")
		}
		if message.Content.Text == "" && len(message.Content.ArtifactIDs) == 0 {
			if result.Coverage != HistoryPartial {
				return ProviderReadResult{}, errors.New("unavailable message content requires partial history")
			}
		} else if err := message.Content.validateContent(); err != nil {
			return ProviderReadResult{}, fmt.Errorf("history message content: %w", err)
		}
		if err := validateObservationReferences(*message); err != nil {
			return ProviderReadResult{}, err
		}
		metadata, err := NormalizeOpaqueObject(message.Metadata)
		if err != nil {
			return ProviderReadResult{}, fmt.Errorf("history metadata: %w", err)
		}
		message.Metadata = metadata
	}
	return result, nil
}

func validateObservationReferences(message ProviderMessageObservation) error {
	if err := boundedResultText(message.MessageID, 2048); err != nil {
		return fmt.Errorf("history message ID: %w", err)
	}
	if message.ReplyTo != nil {
		if message.ReplyTo.MessageID == "" || boundedResultText(message.ReplyTo.MessageID, 2048) != nil {
			return errors.New("invalid history reply reference")
		}
		if err := validateReplyDestination(message.ReplyTo.Destination); err != nil {
			return err
		}
	}
	if err := validateReplyDestination(message.ReplyChannel); err != nil {
		return err
	}
	if message.Author != nil {
		if err := boundedResultText(message.Author.Ref, 512); err != nil {
			return fmt.Errorf("history author reference: %w", err)
		}
		if err := boundedResultText(message.Author.DisplayName, 256); err != nil {
			return fmt.Errorf("history author name: %w", err)
		}
	}
	return nil
}

func validateReplyDestination(destination *ReplyDestination) error {
	if destination == nil {
		return nil
	}
	if !registryname.Valid(destination.ImplementationKey) ||
		destination.ProviderRef == "" || destination.ProviderRefKind == "" {
		return errors.New("reply channel requires its implementation and provider address")
	}
	for _, field := range []struct {
		value string
		limit int
	}{
		{destination.ProviderRef, 2048}, {destination.ProviderRefKind, 128}, {destination.DisplayName, 512},
	} {
		if err := boundedResultText(field.value, field.limit); err != nil {
			return fmt.Errorf("reply channel address: %w", err)
		}
	}
	metadata, err := NormalizeOpaqueObject(destination.ProviderMetadata)
	if err != nil {
		return fmt.Errorf("reply channel metadata: %w", err)
	}
	destination.ProviderMetadata = metadata
	return nil
}

func decodeMessageResult(raw json.RawMessage, result any) (map[string]any, error) {
	fields, err := jsoncanonical.ParseObject(raw, int(MaxOperationResponseBytes))
	if err != nil {
		return nil, fmt.Errorf("invalid channel result: %w", err)
	}
	if err := validateResultFields(fields); err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(result); err != nil {
		return nil, fmt.Errorf("invalid channel result: %w", err)
	}
	return fields, nil
}

// These fixed transport DTOs use lowercase ASCII field names and non-null
// values. encoding/json otherwise accepts case-insensitive aliases and null
// scalars. Metadata is the only open object; its contents remain provider-owned.
func validateResultFields(value any) error {
	switch value := value.(type) {
	case map[string]any:
		for key, field := range value {
			if strings.IndexFunc(key, func(r rune) bool {
				return (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '_'
			}) >= 0 {
				return errors.New("channel result field names must match their exact lowercase spelling")
			}
			if key == "metadata" || key == "provider_metadata" {
				if _, ok := field.(map[string]any); !ok {
					return errors.New("channel result metadata must be an object")
				}
				continue
			}
			if err := validateResultFields(field); err != nil {
				return err
			}
		}
	case []any:
		for _, item := range value {
			if err := validateResultFields(item); err != nil {
				return err
			}
		}
	case nil:
		return errors.New("channel result fields cannot be null")
	}
	return nil
}

func validPublication(value MessagePublication) bool {
	return value == MessagePublished || value == MessageDraft
}

func boundedResultText(value string, limit int) error {
	if len(value) > limit {
		return errors.New("text exceeds its size limit")
	}
	return dbsafe.Text(value)
}
