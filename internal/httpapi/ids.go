package httpapi

import (
	"fmt"

	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage"
)

func publicID(kind publicid.Kind, id storage.ID) (string, error) {
	encoded, err := publicid.Encode(kind, id)
	if err != nil {
		return "", fmt.Errorf("public id encoding failed for %s %s: %w", kind, id, err)
	}
	return encoded, nil
}

func parsePublicID(kind publicid.Kind, value string) (storage.ID, error) {
	id, err := publicid.Decode(kind, value)
	if err != nil {
		return storage.NilID, err
	}
	return id, nil
}

func parseOpenAPIPublicID(kind publicid.Kind, raw string) (storage.ID, bool) {
	id, err := parsePublicID(kind, raw)
	if err != nil {
		return storage.NilID, false
	}
	return id, true
}

func idOrEmpty(kind publicid.Kind, id storage.ID) (string, error) {
	if id == storage.NilID {
		return "", nil
	}
	return publicID(kind, id)
}

func idOrNil(kind publicid.Kind, id storage.ID) (*string, error) {
	value, err := idOrEmpty(kind, id)
	if err != nil || value == "" {
		return nil, err
	}
	return &value, nil
}
