package httpjson

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
)

func Write(w http.ResponseWriter, status int, body any) {
	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(body); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(buf.Bytes())
}

func DecodeStrictRequiredBytes(body []byte, dst any) error {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		return err
	}
	if err := rejectTrailing(decoder); err != nil {
		return err
	}
	return nil
}

func DecodeAllowed(r *http.Request, dst any, allowedFields, pathFields map[string]bool, objectFields ...string) error {
	raw, err := DecodeAllowedRaw(r, allowedFields, pathFields, objectFields...)
	if err != nil {
		return err
	}
	body, err := json.Marshal(raw)
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	decoder.UseNumber()
	return decoder.Decode(dst)
}

func DecodeAllowedRaw(
	r *http.Request,
	allowedFields, pathFields map[string]bool,
	objectFields ...string,
) (map[string]json.RawMessage, error) {
	var raw map[string]json.RawMessage
	decoder := json.NewDecoder(r.Body)
	if err := decoder.Decode(&raw); err != nil {
		if errors.Is(err, io.EOF) {
			return map[string]json.RawMessage{}, nil
		}
		return nil, err
	}
	if err := rejectTrailing(decoder); err != nil {
		return nil, err
	}
	if raw == nil {
		return nil, errors.New("request body must be a JSON object")
	}
	if err := validateAllowedJSONFields(raw, allowedFields, pathFields, ""); err != nil {
		return nil, err
	}
	for _, field := range objectFields {
		value, ok := raw[field]
		if !ok {
			continue
		}
		var object map[string]json.RawMessage
		if err := json.Unmarshal(value, &object); err != nil || object == nil {
			return nil, errors.New(field + " must be a JSON object")
		}
	}
	return raw, nil
}

func validateAllowedJSONFields(
	raw map[string]json.RawMessage,
	allowedFields, pathFields map[string]bool,
	prefix string,
) error {
	for field := range pathFields {
		if _, exists := raw[field]; exists {
			return errors.New(prefix + field + " belongs in the request path")
		}
	}
	for field := range raw {
		if !allowedFields[field] {
			return errors.New("unknown field: " + prefix + field)
		}
	}
	return nil
}

func rejectTrailing(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); err == nil {
		return errors.New("request body must contain a single JSON value")
	} else if !errors.Is(err, io.EOF) {
		return errors.New("request body must contain a single JSON value")
	}
	return nil
}
