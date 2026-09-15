package jsoncanonical

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"unicode/utf8"
)

// MaxDepth bounds the number of nested JSON containers accepted by Decode.
const MaxDepth = 128

// ParseObject decodes exactly one JSON object, subject to a positive byte limit
// (including whitespace) and MaxDepth. It rejects duplicate decoded member names
// at every depth, including inside arrays, before they can be lost in a map.
// Numbers remain json.Number; neither raw nor its backing array is modified.
func ParseObject(raw json.RawMessage, maxBytes int) (map[string]any, error) {
	if maxBytes <= 0 {
		return nil, errors.New("JSON byte limit must be positive")
	}
	if len(raw) > maxBytes {
		return nil, fmt.Errorf("JSON exceeds %d byte limit", maxBytes)
	}
	value, err := Decode(raw)
	if err != nil {
		return nil, err
	}
	object, ok := value.(map[string]any)
	if !ok {
		return nil, errors.New("JSON root must be an object")
	}
	return object, nil
}

// Decode decodes one JSON value without duplicate decoded keys, trailing data,
// invalid UTF-8, or floating-point coercion. Container depth is bounded by
// MaxDepth. Callers accepting untrusted bytes must also impose a byte limit;
// ParseObject does both for object-valued inputs.
func Decode(raw json.RawMessage) (any, error) {
	if !utf8.Valid(raw) {
		return nil, errors.New("JSON contains invalid UTF-8")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	value, err := readValue(decoder, "", 0)
	if err != nil {
		return nil, err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		if err != nil {
			return nil, fmt.Errorf("trailing JSON data: %w", err)
		}
		return nil, errors.New("trailing JSON value")
	}
	return value, nil
}

func readValue(decoder *json.Decoder, path string, depth int) (any, error) {
	token, err := decoder.Token()
	if err != nil {
		return nil, fmt.Errorf("JSON at %q: %w", path, err)
	}
	delim, container := token.(json.Delim)
	if !container {
		return token, nil
	}
	if depth >= MaxDepth {
		return nil, fmt.Errorf("JSON at %q exceeds container depth %d", path, MaxDepth)
	}
	switch delim {
	case '{':
		object := make(map[string]any)
		for decoder.More() {
			token, err := decoder.Token()
			if err != nil {
				return nil, err
			}
			key, ok := token.(string)
			if !ok {
				return nil, fmt.Errorf("JSON at %q: expected object member name", path)
			}
			memberPath := path + "/" + strings.ReplaceAll(strings.ReplaceAll(key, "~", "~0"), "/", "~1")
			if _, exists := object[key]; exists {
				return nil, fmt.Errorf("duplicate JSON object key %q at %q", key, memberPath)
			}
			value, err := readValue(decoder, memberPath, depth+1)
			if err != nil {
				return nil, err
			}
			object[key] = value
		}
		if _, err := decoder.Token(); err != nil {
			return nil, err
		}
		return object, nil
	case '[':
		array := make([]any, 0)
		for decoder.More() {
			value, err := readValue(decoder, path+"/"+strconv.Itoa(len(array)), depth+1)
			if err != nil {
				return nil, err
			}
			array = append(array, value)
		}
		if _, err := decoder.Token(); err != nil {
			return nil, err
		}
		return array, nil
	default:
		return nil, fmt.Errorf("JSON at %q: unexpected delimiter %q", path, delim)
	}
}
