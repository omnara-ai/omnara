package jsoncanonical

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"math/big"
)

func Normalize(raw json.RawMessage) (json.RawMessage, error) {
	value, err := decode(raw)
	if err != nil {
		return nil, err
	}
	return json.Marshal(value)
}

func Equal(left, right json.RawMessage) bool {
	leftValue, leftErr := decode(left)
	rightValue, rightErr := decode(right)
	return leftErr == nil && rightErr == nil && valuesEqual(leftValue, rightValue)
}

func decode(raw json.RawMessage) (any, error) {
	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, errors.New("trailing JSON value")
		}
		return nil, err
	}
	return value, nil
}

func valuesEqual(left, right any) bool {
	switch left := left.(type) {
	case nil:
		return right == nil
	case bool:
		right, ok := right.(bool)
		return ok && left == right
	case string:
		right, ok := right.(string)
		return ok && left == right
	case json.Number:
		right, ok := right.(json.Number)
		return ok && numbersEqual(left, right)
	case []any:
		right, ok := right.([]any)
		if !ok || len(left) != len(right) {
			return false
		}
		for index := range left {
			if !valuesEqual(left[index], right[index]) {
				return false
			}
		}
		return true
	case map[string]any:
		right, ok := right.(map[string]any)
		if !ok || len(left) != len(right) {
			return false
		}
		for key, value := range left {
			other, ok := right[key]
			if !ok || !valuesEqual(value, other) {
				return false
			}
		}
		return true
	default:
		return false
	}
}

func numbersEqual(left, right json.Number) bool {
	var leftValue, rightValue big.Rat
	if _, ok := leftValue.SetString(string(left)); !ok {
		return false
	}
	if _, ok := rightValue.SetString(string(right)); !ok {
		return false
	}
	return leftValue.Cmp(&rightValue) == 0
}
