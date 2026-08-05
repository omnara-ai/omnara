package apivariantbody

import (
	"bytes"
	"encoding/json"
	"fmt"
)

const apiVariantOptionsObjectMergeError = "api_variant_options must be a JSON object " +
	"to merge into a provider request"

func MarshalWithAPIVariantOptions(
	apiVariantOptions json.RawMessage,
	payload any,
	adapterOwnedFields ...string,
) (json.RawMessage, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	apiVariantOptions = bytes.TrimSpace(apiVariantOptions)
	if len(apiVariantOptions) == 0 || bytes.Equal(apiVariantOptions, []byte("null")) {
		return body, nil
	}
	var base map[string]json.RawMessage
	if err := json.Unmarshal(body, &base); err != nil {
		return nil, err
	}
	var extra map[string]json.RawMessage
	if err := json.Unmarshal(apiVariantOptions, &extra); err != nil {
		return nil, fmt.Errorf("%s: %w", apiVariantOptionsObjectMergeError, err)
	}
	if extra == nil {
		return nil, fmt.Errorf("%s", apiVariantOptionsObjectMergeError)
	}
	merged := make(map[string]json.RawMessage, len(extra)+len(base))
	for key, value := range base {
		merged[key] = value
	}
	owned := make(map[string]bool, len(adapterOwnedFields))
	for _, key := range adapterOwnedFields {
		owned[key] = true
	}
	for key, value := range extra {
		if owned[key] {
			continue
		}
		merged[key] = value
	}
	return json.Marshal(merged)
}
