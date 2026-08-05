package tools

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/omnara-ai/omnara/internal/storage"
)

type toolResultContent struct {
	parts []toolResultPart
	isSet bool
}

type toolResultPart interface {
	toolResultPart()
}

type toolResultTextPart struct {
	text string
}

type toolResultStructuredPart struct {
	value json.RawMessage
}

type toolResultMediaPart struct {
	artifactID storage.ID
}

func (toolResultTextPart) toolResultPart()       {}
func (toolResultStructuredPart) toolResultPart() {}
func (toolResultMediaPart) toolResultPart()      {}

func newToolResultContent(parts ...toolResultPart) toolResultContent {
	return toolResultContent{
		parts: append([]toolResultPart(nil), parts...),
		isSet: true,
	}
}

func structuredToolResultContent(value any) (toolResultContent, error) {
	part, err := structuredToolResultPart(value)
	if err != nil {
		return toolResultContent{}, err
	}
	return newToolResultContent(part), nil
}

func textToolResultPart(text string) toolResultPart {
	return toolResultTextPart{text: text}
}

func structuredToolResultPart(value any) (toolResultPart, error) {
	raw, err := marshalJSON(value)
	if err != nil {
		return nil, fmt.Errorf("marshal structured tool result content: %w", err)
	}
	return toolResultStructuredPart{value: raw}, nil
}

func mediaToolResultPart(artifactID storage.ID) (toolResultPart, error) {
	if artifactID == storage.NilID {
		return nil, errors.New("tool result media requires an artifact")
	}
	return toolResultMediaPart{artifactID: artifactID}, nil
}

func (content toolResultContent) contentParts() (json.RawMessage, error) {
	if !content.isSet {
		return nil, errors.New("tool result content was not set")
	}
	parts := make([]json.RawMessage, 0, len(content.parts))
	for index, part := range content.parts {
		var (
			raw json.RawMessage
			err error
		)
		switch part := part.(type) {
		case toolResultTextPart:
			raw, err = marshalJSON(struct {
				Type string `json:"type"`
				Text string `json:"text"`
			}{
				Type: "text",
				Text: part.text,
			})
		case toolResultStructuredPart:
			raw, err = marshalJSON(struct {
				Type  string          `json:"type"`
				Value json.RawMessage `json:"value"`
			}{
				Type:  "structured_data",
				Value: part.value,
			})
		case toolResultMediaPart:
			raw, err = marshalJSON(struct {
				Type       string `json:"type"`
				ArtifactID string `json:"artifact_id"`
			}{
				Type:       "media_ref",
				ArtifactID: part.artifactID.String(),
			})
		case nil:
			err = errors.New("tool result content contains a nil part")
		default:
			err = fmt.Errorf("unsupported tool result content part %T", part)
		}
		if err != nil {
			return nil, fmt.Errorf("marshal tool result content part %d: %w", index, err)
		}
		parts = append(parts, raw)
	}
	return marshalJSON(parts)
}
