package httpapi

import (
	"fmt"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/publicid"
)

func publicID(kind publicid.Kind, id uuid.UUID) (string, error) {
	encoded, err := publicid.Encode(kind, id)
	if err != nil {
		return "", fmt.Errorf("public id encoding failed for %s %s: %w", kind, id, err)
	}
	return encoded, nil
}

func parseOpenAPIPublicID(kind publicid.Kind, raw string) (uuid.UUID, bool) {
	id, err := publicid.Decode(kind, raw)
	if err != nil {
		return uuid.Nil, false
	}
	return id, true
}

func idOrEmpty(kind publicid.Kind, id uuid.UUID) (string, error) {
	if id == uuid.Nil {
		return "", nil
	}
	return publicID(kind, id)
}

func idOrNil(kind publicid.Kind, id uuid.UUID) (*string, error) {
	value, err := idOrEmpty(kind, id)
	if err != nil || value == "" {
		return nil, err
	}
	return &value, nil
}
