package tools

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
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
	// hidden keeps the value out of model context while it stays in the stored result.
	hidden bool
}

type toolResultMediaPart struct {
	artifactID uuid.UUID
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

// fileDigestToolResultPart records a file's digest for memory read tracking without showing it to the model.
func fileDigestToolResultPart(digest string) (toolResultPart, error) {
	raw, err := marshalJSON(map[string]string{"digest": digest})
	if err != nil {
		return nil, fmt.Errorf("marshal file digest: %w", err)
	}
	return toolResultStructuredPart{value: raw, hidden: true}, nil
}

func mediaToolResultPart(artifactID uuid.UUID) (toolResultPart, error) {
	if artifactID == uuid.Nil {
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
				Type    string          `json:"type"`
				Value   json.RawMessage `json:"value"`
				Exclude bool            `json:"exclude_from_model_context,omitempty"`
			}{
				Type:    "structured_data",
				Value:   part.value,
				Exclude: part.hidden,
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
