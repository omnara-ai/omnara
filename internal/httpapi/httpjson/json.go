package httpjson

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"unicode/utf8"
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
	if err := ValidateUnicode(body); err != nil {
		return err
	}
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
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, err
	}
	if err := ValidateUnicode(body); err != nil {
		return nil, err
	}
	var raw map[string]json.RawMessage
	decoder := json.NewDecoder(bytes.NewReader(body))
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

func ValidateUnicode(raw []byte) error {
	if !utf8.Valid(raw) {
		return errors.New("request body must be valid UTF-8")
	}
	inString := false
	for index := 0; index < len(raw); index++ {
		switch raw[index] {
		case '"':
			inString = !inString
		case '\\':
			if !inString || index+1 >= len(raw) {
				continue
			}
			if raw[index+1] != 'u' {
				index++
				continue
			}
			codeUnit, ok := jsonHexCodeUnit(raw[index+2:])
			if !ok {
				continue
			}
			switch {
			case codeUnit >= 0xd800 && codeUnit <= 0xdbff:
				if index+12 > len(raw) || raw[index+6] != '\\' || raw[index+7] != 'u' {
					return errors.New("request body must use valid Unicode scalar values")
				}
				low, ok := jsonHexCodeUnit(raw[index+8:])
				if !ok || low < 0xdc00 || low > 0xdfff {
					return errors.New("request body must use valid Unicode scalar values")
				}
				index += 11
			case codeUnit >= 0xdc00 && codeUnit <= 0xdfff:
				return errors.New("request body must use valid Unicode scalar values")
			default:
				index += 5
			}
		}
	}
	return nil
}

func jsonHexCodeUnit(raw []byte) (uint16, bool) {
	if len(raw) < 4 {
		return 0, false
	}
	var value uint16
	for _, char := range raw[:4] {
		digit := jsonHexValue(char)
		if digit > 15 {
			return 0, false
		}
		value = value*16 + digit
	}
	return value, true
}

func jsonHexValue(char byte) uint16 {
	switch {
	case char >= '0' && char <= '9':
		return uint16(char - '0')
	case char >= 'a' && char <= 'f':
		return uint16(char-'a') + 10
	case char >= 'A' && char <= 'F':
		return uint16(char-'A') + 10
	default:
		return 16
	}
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
