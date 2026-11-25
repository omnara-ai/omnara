package httpapi

import (
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/httpapi/apierror"
	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

func secretIDsFromPointer[T openapi.SecretID | *openapi.SecretID](value *map[string]T) (json.RawMessage, error) {
	raw, err := rawJSONFromPointer(value)
	if err != nil {
		return nil, err
	}
	decoded, err := publicid.DecodeMapJSON(publicid.KindSecret, raw)
	if err != nil {
		return nil, apierror.FromError(fmt.Errorf("%w: secret_env: %w", storeerr.ErrInvalidRequest, err))
	}
	return decoded, nil
}

func publicSecretIDs[T openapi.SecretID | *openapi.SecretID](raw json.RawMessage, dest *map[string]T) error {
	encoded, err := publicid.EncodeMapJSON(publicid.KindSecret, raw)
	if err != nil {
		return err
	}
	return json.Unmarshal(encoded, dest)
}

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
