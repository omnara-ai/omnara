package executionstore

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/omnara-ai/omnara/internal/dbsafe"
	"github.com/omnara-ai/omnara/internal/jsoncanonical"
	"github.com/omnara-ai/omnara/internal/resourcemeta"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

func parseAgentInputContentBlocks(
	contentBlocks json.RawMessage,
) ([]CreateContentBlockInput, error) {
	rawBlocks, err := contentBlockArray(contentBlocks)
	if err != nil {
		return nil, err
	}
	if len(rawBlocks) == 0 {
		return nil, storeerr.InvalidRequest(errors.New(
			"content_blocks must contain at least one block",
		))
	}
	blocks := make([]CreateContentBlockInput, 0, len(rawBlocks))
	for ordinal, raw := range rawBlocks {
		kind, metadata, fields, err := decodeContentBlock(raw)
		if err != nil {
			return nil, storeerr.InvalidRequest(
				fmt.Errorf("agent input content block %d: %w", ordinal, err),
			)
		}
		var block CreateContentBlockInput
		switch kind {
		case "text":
			block, err = parseTextContentBlock(fields)
		case "media_ref":
			block, err = parseMediaRefContentBlock(fields)
		default:
			err = fmt.Errorf("unsupported type %q", kind)
		}
		if err != nil {
			return nil, storeerr.InvalidRequest(
				fmt.Errorf("agent input content block %d: %w", ordinal, err),
			)
		}
		block.Ordinal = int32(ordinal)
		block.Metadata = metadata
		blocks = append(blocks, block)
	}
	return blocks, nil
}

func parseToolResultContentBlocks(
	contentBlocks json.RawMessage,
) ([]CreateContentBlockInput, error) {
	rawBlocks, err := contentBlockArray(contentBlocks)
	if err != nil {
		return nil, err
	}
	blocks := make([]CreateContentBlockInput, 0, len(rawBlocks))
	for ordinal, raw := range rawBlocks {
		kind, metadata, fields, err := decodeContentBlock(raw)
		if err != nil {
			return nil, storeerr.InvalidRequest(
				fmt.Errorf("tool result content block %d: %w", ordinal, err),
			)
		}
		var block CreateContentBlockInput
		switch kind {
		case "text":
			block, err = parseTextContentBlock(fields)
		case "media_ref":
			block, err = parseMediaRefContentBlock(fields)
		case "structured_data":
			block, err = parseStructuredDataContentBlock(fields)
		default:
			err = fmt.Errorf("unsupported type %q", kind)
		}
		if err != nil {
			return nil, storeerr.InvalidRequest(
				fmt.Errorf("tool result content block %d: %w", ordinal, err),
			)
		}
		block.Ordinal = int32(ordinal)
		block.Metadata = metadata
		blocks = append(blocks, block)
	}
	return blocks, nil
}

func contentBlockArray(contentBlocks json.RawMessage) ([]json.RawMessage, error) {
	contentBlocks = normalizedJSONArray(contentBlocks)
	var rawBlocks []json.RawMessage
	if err := json.Unmarshal(contentBlocks, &rawBlocks); err != nil ||
		rawBlocks == nil {
		if err == nil {
			err = errors.New("must be a JSON array")
		}
		return nil, storeerr.InvalidRequest(fmt.Errorf("content_blocks: %w", err))
	}
	return rawBlocks, nil
}

func decodeContentBlock(
	raw json.RawMessage,
) (string, resourcemeta.Metadata, map[string]json.RawMessage, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return "", nil, nil, errors.New("must be a JSON object")
	}
	rawType, ok := fields["type"]
	if !ok {
		return "", nil, nil, errors.New("requires type")
	}
	var kind string
	if err := json.Unmarshal(rawType, &kind); err != nil {
		return "", nil, nil, errors.New("type must be a string")
	}
	if kind == "" {
		return "", nil, nil, errors.New("requires type")
	}
	metadata, err := resourcemeta.FromJSON(fields["metadata"])
	if err != nil {
		return "", nil, nil, fmt.Errorf("invalid metadata: %w", err)
	}
	return kind, metadata, fields, nil
}

func parseTextContentBlock(
	fields map[string]json.RawMessage,
) (CreateContentBlockInput, error) {
	if err := rejectContentBlockFields(fields, "text"); err != nil {
		return CreateContentBlockInput{}, err
	}
	rawText, ok := fields["text"]
	if !ok || string(rawText) == "null" {
		return CreateContentBlockInput{}, errors.New("text is required")
	}
	var text string
	if err := json.Unmarshal(rawText, &text); err != nil {
		return CreateContentBlockInput{}, errors.New("text must be a string")
	}
	if err := dbsafe.Text(text); err != nil {
		return CreateContentBlockInput{}, fmt.Errorf("text %w", err)
	}
	return CreateContentBlockInput{
		BlockKind:   ContentBlockKindText,
		TextContent: text,
	}, nil
}

func parseMediaRefContentBlock(
	fields map[string]json.RawMessage,
) (CreateContentBlockInput, error) {
	if err := rejectContentBlockFields(fields, "artifact_id", "exclude_from_model_context"); err != nil {
		return CreateContentBlockInput{}, err
	}
	artifactID, err := requiredArtifactID(fields["artifact_id"])
	if err != nil {
		return CreateContentBlockInput{}, err
	}
	excludeFromModelContext, err := optionalBool(fields["exclude_from_model_context"])
	if err != nil {
		return CreateContentBlockInput{}, fmt.Errorf("exclude_from_model_context %w", err)
	}
	return CreateContentBlockInput{
		BlockKind:               ContentBlockKindArtifact,
		ArtifactID:              artifactID,
		ExcludeFromModelContext: excludeFromModelContext,
	}, nil
}

func optionalBool(raw json.RawMessage) (bool, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return false, nil
	}
	var value bool
	if err := json.Unmarshal(raw, &value); err != nil {
		return false, errors.New("must be a boolean")
	}
	return value, nil
}

func parseStructuredDataContentBlock(
	fields map[string]json.RawMessage,
) (CreateContentBlockInput, error) {
	if err := rejectContentBlockFields(fields, "value"); err != nil {
		return CreateContentBlockInput{}, err
	}
	value, ok := fields["value"]
	if !ok {
		return CreateContentBlockInput{}, errors.New("value is required")
	}
	value, err := normalizeContentBlockJSONForStorage(value)
	if err != nil {
		return CreateContentBlockInput{}, fmt.Errorf("value %w", err)
	}
	return CreateContentBlockInput{
		BlockKind:      ContentBlockKindStructuredData,
		StructuredData: value,
	}, nil
}

func normalizeContentBlockJSONForStorage(value json.RawMessage) (json.RawMessage, error) {
	normalized, err := jsoncanonical.Normalize(value)
	if err != nil {
		return nil, errors.New("is not valid JSON")
	}
	if err := dbsafe.JSONStrings(normalized); err != nil {
		return nil, fmt.Errorf("JSON string %w", err)
	}
	return normalized, nil
}

func rejectContentBlockFields(
	fields map[string]json.RawMessage,
	allowed ...string,
) error {
	for field := range fields {
		if field == "type" || field == "metadata" {
			continue
		}
		found := false
		for _, allowedField := range allowed {
			if field == allowedField {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("unsupported field %q", field)
		}
	}
	return nil
}

func requiredArtifactID(raw json.RawMessage) (ID, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return NilID, errors.New("artifact_id is required")
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return NilID, errors.New("artifact_id must be a string")
	}
	id := parseUUIDText(value)
	if isNilID(id) {
		return NilID, errors.New("artifact_id must be a valid artifact ID")
	}
	return id, nil
}

func marshalAgentInputContentBlocks(
	blocks []CreateContentBlockInput,
) (json.RawMessage, error) {
	if len(blocks) == 0 {
		return nil, errors.New("agent input content blocks must not be empty")
	}
	parts := make([]json.RawMessage, 0, len(blocks))
	for ordinal, block := range blocks {
		part, err := marshalAgentInputContentBlock(block)
		if err != nil {
			return nil, fmt.Errorf("marshal agent input content block %d: %w", ordinal, err)
		}
		parts = append(parts, part)
	}
	return marshalJSON(parts)
}

func marshalAgentInputContentBlock(
	block CreateContentBlockInput,
) (json.RawMessage, error) {
	metadata := contentBlockMetadataForOutput(block.Metadata)
	switch block.BlockKind {
	case ContentBlockKindText:
		return marshalJSON(struct {
			Type     string                `json:"type"`
			Text     string                `json:"text"`
			Metadata resourcemeta.Metadata `json:"metadata,omitempty"`
		}{
			Type:     "text",
			Text:     block.TextContent,
			Metadata: metadata,
		})
	case ContentBlockKindArtifact:
		if isNilID(block.ArtifactID) {
			return nil, errors.New("media_ref requires an artifact")
		}
		return marshalJSON(struct {
			Type                    string                `json:"type"`
			ArtifactID              string                `json:"artifact_id"`
			ExcludeFromModelContext bool                  `json:"exclude_from_model_context,omitempty"`
			Metadata                resourcemeta.Metadata `json:"metadata,omitempty"`
		}{
			Type:                    "media_ref",
			ArtifactID:              block.ArtifactID.String(),
			ExcludeFromModelContext: block.ExcludeFromModelContext,
			Metadata:                metadata,
		})
	default:
		return nil, fmt.Errorf("unsupported block kind %q", block.BlockKind)
	}
}

func marshalToolResultContentBlocks(
	blocks []CreateContentBlockInput,
) (json.RawMessage, error) {
	parts := make([]json.RawMessage, 0, len(blocks))
	for ordinal, block := range blocks {
		part, err := marshalToolResultContentBlock(block)
		if err != nil {
			return nil, fmt.Errorf("marshal tool result content block %d: %w", ordinal, err)
		}
		parts = append(parts, part)
	}
	return marshalJSON(parts)
}

func marshalToolResultContentBlock(
	block CreateContentBlockInput,
) (json.RawMessage, error) {
	metadata := contentBlockMetadataForOutput(block.Metadata)
	switch block.BlockKind {
	case ContentBlockKindText:
		return marshalJSON(struct {
			Type     string                `json:"type"`
			Text     string                `json:"text"`
			Metadata resourcemeta.Metadata `json:"metadata,omitempty"`
		}{
			Type:     "text",
			Text:     block.TextContent,
			Metadata: metadata,
		})
	case ContentBlockKindStructuredData:
		if len(block.StructuredData) == 0 {
			return nil, errors.New("structured_data requires a value")
		}
		return marshalJSON(struct {
			Type     string                `json:"type"`
			Value    json.RawMessage       `json:"value"`
			Metadata resourcemeta.Metadata `json:"metadata,omitempty"`
		}{
			Type:     "structured_data",
			Value:    block.StructuredData,
			Metadata: metadata,
		})
	case ContentBlockKindArtifact:
		if isNilID(block.ArtifactID) {
			return nil, errors.New("media_ref requires an artifact")
		}
		return marshalJSON(struct {
			Type                    string                `json:"type"`
			ArtifactID              string                `json:"artifact_id"`
			ExcludeFromModelContext bool                  `json:"exclude_from_model_context,omitempty"`
			Metadata                resourcemeta.Metadata `json:"metadata,omitempty"`
		}{
			Type:                    "media_ref",
			ArtifactID:              block.ArtifactID.String(),
			ExcludeFromModelContext: block.ExcludeFromModelContext,
			Metadata:                metadata,
		})
	default:
		return nil, fmt.Errorf("unsupported block kind %q", block.BlockKind)
	}
}

func contentBlockMetadataForOutput(metadata resourcemeta.Metadata) resourcemeta.Metadata {
	if len(metadata) == 0 {
		return nil
	}
	return metadata
}
