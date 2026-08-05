package executionstore

import (
	"encoding/json"
	"errors"
	"fmt"

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
		kind, fields, err := decodeContentBlock(raw)
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
		kind, fields, err := decodeContentBlock(raw)
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
) (string, map[string]json.RawMessage, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return "", nil, errors.New("must be a JSON object")
	}
	rawType, ok := fields["type"]
	if !ok {
		return "", nil, errors.New("requires type")
	}
	var kind string
	if err := json.Unmarshal(rawType, &kind); err != nil {
		return "", nil, errors.New("type must be a string")
	}
	if kind == "" {
		return "", nil, errors.New("requires type")
	}
	return kind, fields, nil
}

func parseTextContentBlock(
	fields map[string]json.RawMessage,
) (CreateContentBlockInput, error) {
	if err := rejectContentBlockFields(fields, "type", "text"); err != nil {
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
	return CreateContentBlockInput{
		BlockKind:   ContentBlockKindText,
		TextContent: text,
	}, nil
}

func parseMediaRefContentBlock(
	fields map[string]json.RawMessage,
) (CreateContentBlockInput, error) {
	if err := rejectContentBlockFields(fields, "type", "artifact_id"); err != nil {
		return CreateContentBlockInput{}, err
	}
	artifactID, err := requiredArtifactID(fields["artifact_id"])
	if err != nil {
		return CreateContentBlockInput{}, err
	}
	return CreateContentBlockInput{
		BlockKind:  ContentBlockKindArtifact,
		ArtifactID: artifactID,
	}, nil
}

func parseStructuredDataContentBlock(
	fields map[string]json.RawMessage,
) (CreateContentBlockInput, error) {
	if err := rejectContentBlockFields(fields, "type", "value"); err != nil {
		return CreateContentBlockInput{}, err
	}
	value, ok := fields["value"]
	if !ok {
		return CreateContentBlockInput{}, errors.New("value is required")
	}
	return CreateContentBlockInput{
		BlockKind:      ContentBlockKindStructuredData,
		StructuredData: value,
	}, nil
}

func rejectContentBlockFields(
	fields map[string]json.RawMessage,
	allowed ...string,
) error {
	for field := range fields {
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
	switch block.BlockKind {
	case ContentBlockKindText:
		return marshalJSON(struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}{
			Type: "text",
			Text: block.TextContent,
		})
	case ContentBlockKindArtifact:
		if isNilID(block.ArtifactID) {
			return nil, errors.New("media_ref requires an artifact")
		}
		return marshalJSON(struct {
			Type       string `json:"type"`
			ArtifactID string `json:"artifact_id"`
		}{
			Type:       "media_ref",
			ArtifactID: block.ArtifactID.String(),
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
	switch block.BlockKind {
	case ContentBlockKindText:
		return marshalJSON(struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}{
			Type: "text",
			Text: block.TextContent,
		})
	case ContentBlockKindStructuredData:
		if len(block.StructuredData) == 0 {
			return nil, errors.New("structured_data requires a value")
		}
		return marshalJSON(struct {
			Type  string          `json:"type"`
			Value json.RawMessage `json:"value"`
		}{
			Type:  "structured_data",
			Value: block.StructuredData,
		})
	case ContentBlockKindArtifact:
		if isNilID(block.ArtifactID) {
			return nil, errors.New("media_ref requires an artifact")
		}
		return marshalJSON(struct {
			Type       string `json:"type"`
			ArtifactID string `json:"artifact_id"`
		}{
			Type:       "media_ref",
			ArtifactID: block.ArtifactID.String(),
		})
	default:
		return nil, fmt.Errorf("unsupported block kind %q", block.BlockKind)
	}
}
