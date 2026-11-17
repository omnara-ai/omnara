package patch

import "encoding/json"

type NullableInt struct {
	Set   bool
	Value *int
}

type NullableBool struct {
	Set   bool
	Value *bool
}

type NullableJSON struct {
	Set   bool
	Value *json.RawMessage
}
