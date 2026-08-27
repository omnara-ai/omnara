package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
	"github.com/omnara-ai/omnara/internal/modelenvelope"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/resourcemeta"
	"github.com/omnara-ai/omnara/internal/storage"
)

type storedContentBlockType struct {
	Type string `json:"type"`
}

type storedTextContentBlock struct {
	Type     string                `json:"type"`
	Text     *string               `json:"text"`
	Metadata resourcemeta.Metadata `json:"metadata,omitempty"`
}

type storedMediaRefContentBlock struct {
	Type                    string                `json:"type"`
	ArtifactID              string                `json:"artifact_id"`
	ExcludeFromModelContext *bool                 `json:"exclude_from_model_context,omitempty"`
	Metadata                resourcemeta.Metadata `json:"metadata,omitempty"`
}

type storedToolCallContentBlock struct {
	Type       string                `json:"type"`
	ToolCallID string                `json:"tool_call_id"`
	ToolType   string                `json:"tool_type"`
	Name       string                `json:"name"`
	Input      json.RawMessage       `json:"input"`
	Metadata   resourcemeta.Metadata `json:"metadata,omitempty"`
}

type storedStructuredDataContentBlock struct {
	Type     string                `json:"type"`
	Value    json.RawMessage       `json:"value"`
	Metadata resourcemeta.Metadata `json:"metadata,omitempty"`
}

func decodeStoredContentBlocks(raw json.RawMessage) ([]json.RawMessage, error) {
	var blocks []json.RawMessage
	if len(raw) == 0 {
		return nil, errors.New("content blocks are required")
	}
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return nil, fmt.Errorf("decode content blocks: %w", err)
	}
	if blocks == nil {
		return nil, errors.New("content blocks must be an array")
	}
	return blocks, nil
}

func storedContentBlockTypeFor(
	raw json.RawMessage,
	index int,
) (string, error) {
	var discriminator storedContentBlockType
	if err := json.Unmarshal(raw, &discriminator); err != nil {
		return "", fmt.Errorf("decode content block %d type: %w", index, err)
	}
	if discriminator.Type == "" {
		return "", fmt.Errorf("content block %d is missing type", index)
	}
	return discriminator.Type, nil
}

func decodeStoredContentBlock(
	raw json.RawMessage,
	index int,
	out any,
) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(out); err != nil {
		return fmt.Errorf("decode content block %d: %w", index, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			err = errors.New("multiple JSON values")
		}
		return fmt.Errorf("decode content block %d: %w", index, err)
	}
	return nil
}

func publicAgentInputContentBlocks(
	raw json.RawMessage,
) ([]openapi.AgentInputContentBlock, error) {
	blocks, err := decodeStoredContentBlocks(raw)
	if err != nil {
		return nil, err
	}
	out := make([]openapi.AgentInputContentBlock, 0, len(blocks))
	for index, block := range blocks {
		blockType, err := storedContentBlockTypeFor(block, index)
		if err != nil {
			return nil, err
		}
		var public openapi.AgentInputContentBlock
		switch blockType {
		case "text":
			text, err := publicTextContentBlock(block, index)
			if err != nil {
				return nil, err
			}
			if err := public.FromTextContentBlock(text); err != nil {
				return nil, err
			}
		case "media_ref":
			media, err := publicMediaRefContentBlock(block, index)
			if err != nil {
				return nil, err
			}
			if err := public.FromMediaRefContentBlock(media); err != nil {
				return nil, err
			}
		default:
			return nil, fmt.Errorf(
				"agent input content block %d has unsupported type %q",
				index,
				blockType,
			)
		}
		out = append(out, public)
	}
	return out, nil
}

func publicModelOutputContentBlocks(
	raw json.RawMessage,
) ([]openapi.ModelOutputContentBlock, error) {
	blocks, err := decodeStoredContentBlocks(raw)
	if err != nil {
		return nil, err
	}
	out := make([]openapi.ModelOutputContentBlock, 0, len(blocks))
	for index, block := range blocks {
		blockType, err := storedContentBlockTypeFor(block, index)
		if err != nil {
			return nil, err
		}
		var public openapi.ModelOutputContentBlock
		switch blockType {
		case "text":
			text, err := publicTextContentBlock(block, index)
			if err != nil {
				return nil, err
			}
			if err := public.FromTextContentBlock(text); err != nil {
				return nil, err
			}
		case "media_ref":
			media, err := publicMediaRefContentBlock(block, index)
			if err != nil {
				return nil, err
			}
			if err := public.FromMediaRefContentBlock(media); err != nil {
				return nil, err
			}
		case "reasoning":
			reasoning, metadata, err := decodeStoredTextContentBlock(block, index, "reasoning")
			if err != nil {
				return nil, err
			}
			if err := public.FromReasoningContentBlock(openapi.ReasoningContentBlock{
				Text:     reasoning,
				Metadata: metadata,
			}); err != nil {
				return nil, err
			}
		case "tool_call":
			toolCall, err := publicModelToolCallContentBlock(block, index)
			if err != nil {
				return nil, err
			}
			if err := public.FromModelToolCallContentBlock(toolCall); err != nil {
				return nil, err
			}
		case "error":
			errorText, metadata, err := decodeStoredTextContentBlock(block, index, "error")
			if err != nil {
				return nil, err
			}
			if err := public.FromErrorContentBlock(openapi.ErrorContentBlock{
				Text:     errorText,
				Metadata: metadata,
			}); err != nil {
				return nil, err
			}
		default:
			return nil, fmt.Errorf(
				"model output content block %d has unsupported type %q",
				index,
				blockType,
			)
		}
		out = append(out, public)
	}
	return out, nil
}

func publicToolResultContentBlocks(
	raw json.RawMessage,
) ([]openapi.ToolResultContentBlock, error) {
	blocks, err := decodeStoredContentBlocks(raw)
	if err != nil {
		return nil, err
	}
	out := make([]openapi.ToolResultContentBlock, 0, len(blocks))
	for index, block := range blocks {
		blockType, err := storedContentBlockTypeFor(block, index)
		if err != nil {
			return nil, err
		}
		var public openapi.ToolResultContentBlock
		switch blockType {
		case "text":
			text, err := publicTextContentBlock(block, index)
			if err != nil {
				return nil, err
			}
			if err := public.FromTextContentBlock(text); err != nil {
				return nil, err
			}
		case "media_ref":
			media, err := publicMediaRefContentBlock(block, index)
			if err != nil {
				return nil, err
			}
			if err := public.FromMediaRefContentBlock(media); err != nil {
				return nil, err
			}
		case "structured_data":
			var stored storedStructuredDataContentBlock
			if err := decodeStoredContentBlock(block, index, &stored); err != nil {
				return nil, err
			}
			if stored.Type != "structured_data" {
				return nil, fmt.Errorf(
					"structured data content block %d has type %q",
					index,
					stored.Type,
				)
			}
			value, err := publicJSONValue(
				stored.Value,
				fmt.Sprintf("structured data content block %d value", index),
			)
			if err != nil {
				return nil, err
			}
			if err := public.FromStructuredDataContentBlock(openapi.StructuredDataContentBlock{
				Value:    value,
				Metadata: stored.Metadata,
			}); err != nil {
				return nil, err
			}
		default:
			return nil, fmt.Errorf(
				"tool result content block %d has unsupported type %q",
				index,
				blockType,
			)
		}
		out = append(out, public)
	}
	return out, nil
}

func publicTextContentBlock(
	raw json.RawMessage,
	index int,
) (openapi.TextContentBlock, error) {
	text, metadata, err := decodeStoredTextContentBlock(raw, index, "text")
	if err != nil {
		return openapi.TextContentBlock{}, err
	}
	return openapi.TextContentBlock{Text: text, Metadata: metadata}, nil
}

func decodeStoredTextContentBlock(
	raw json.RawMessage,
	index int,
	expectedType string,
) (string, resourcemeta.Metadata, error) {
	var block storedTextContentBlock
	if err := decodeStoredContentBlock(raw, index, &block); err != nil {
		return "", nil, err
	}
	if block.Type != expectedType {
		return "", nil, fmt.Errorf(
			"%s content block %d has type %q",
			expectedType,
			index,
			block.Type,
		)
	}
	if block.Text == nil {
		return "", nil, fmt.Errorf("%s content block %d is missing text", expectedType, index)
	}
	return *block.Text, block.Metadata, nil
}

func publicMediaRefContentBlock(
	raw json.RawMessage,
	index int,
) (openapi.MediaRefContentBlock, error) {
	var block storedMediaRefContentBlock
	if err := decodeStoredContentBlock(raw, index, &block); err != nil {
		return openapi.MediaRefContentBlock{}, err
	}
	if block.Type != "media_ref" {
		return openapi.MediaRefContentBlock{}, fmt.Errorf(
			"media content block %d has type %q",
			index,
			block.Type,
		)
	}
	id, err := storage.ParseID(block.ArtifactID)
	if err != nil || id == storage.NilID {
		return openapi.MediaRefContentBlock{}, fmt.Errorf(
			"media content block %d has invalid artifact id",
			index,
		)
	}
	artifactID, err := publicID(publicid.KindArtifact, id)
	if err != nil {
		return openapi.MediaRefContentBlock{}, err
	}
	return openapi.MediaRefContentBlock{
		ArtifactId:              artifactID,
		ExcludeFromModelContext: block.ExcludeFromModelContext,
		Metadata:                block.Metadata,
	}, nil
}

func publicModelToolCallContentBlock(
	raw json.RawMessage,
	index int,
) (openapi.ModelToolCallContentBlock, error) {
	var block storedToolCallContentBlock
	if err := decodeStoredContentBlock(raw, index, &block); err != nil {
		return openapi.ModelToolCallContentBlock{}, err
	}
	if block.Type != "tool_call" {
		return openapi.ModelToolCallContentBlock{}, fmt.Errorf(
			"tool call content block %d has type %q",
			index,
			block.Type,
		)
	}
	id, err := storage.ParseID(block.ToolCallID)
	if err != nil || id == storage.NilID {
		return openapi.ModelToolCallContentBlock{}, fmt.Errorf(
			"tool call content block %d has invalid tool call id",
			index,
		)
	}
	toolCallID, err := publicID(publicid.KindToolCall, id)
	if err != nil {
		return openapi.ModelToolCallContentBlock{}, err
	}
	toolType := openapi.ToolCallType(block.ToolType)
	if !toolType.Valid() {
		return openapi.ModelToolCallContentBlock{}, fmt.Errorf(
			"tool call content block %d has invalid tool type %q",
			index,
			block.ToolType,
		)
	}
	if block.Name == "" {
		return openapi.ModelToolCallContentBlock{}, fmt.Errorf(
			"tool call content block %d is missing name",
			index,
		)
	}
	input, err := publicToolInput(
		block.Input,
		fmt.Sprintf("tool call content block %d input", index),
	)
	if err != nil {
		return openapi.ModelToolCallContentBlock{}, err
	}
	return openapi.ModelToolCallContentBlock{
		Input:      input,
		Metadata:   block.Metadata,
		Name:       block.Name,
		ToolCallId: toolCallID,
		ToolType:   toolType,
	}, nil
}

func publicToolInput(
	raw json.RawMessage,
	description string,
) (openapi.ToolInput, error) {
	trimmed := bytes.TrimSpace(raw)
	if err := modelenvelope.ValidateToolInput(trimmed); err != nil {
		return nil, fmt.Errorf("%s must be a JSON object", description)
	}
	return json.RawMessage(bytes.Clone(trimmed)), nil
}

func publicJSONValue(
	raw json.RawMessage,
	description string,
) (openapi.JSONBlob, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return nil, fmt.Errorf("%s is missing", description)
	}
	if !json.Valid(trimmed) {
		return nil, fmt.Errorf("%s is invalid JSON", description)
	}
	if bytes.Equal(trimmed, []byte("null")) {
		return nil, nil //nolint:nilnil // A nil blob is the JSON null representation.
	}
	return json.RawMessage(bytes.Clone(trimmed)), nil
}
