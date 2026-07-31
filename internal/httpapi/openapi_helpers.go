package httpapi

import (
	"encoding/json"

	"github.com/oapi-codegen/nullable"
	"github.com/omnara-ai/omnara/internal/httpapi/apierror"
	"github.com/omnara-ai/omnara/internal/storage/patch"
)

func rawJSONFromPointer[T any](value *T) (json.RawMessage, error) {
	if value == nil {
		return nil, nil
	}
	return json.Marshal(value)
}

func rawJSONFromContentBlocks[T any](value []T) (json.RawMessage, error) {
	if value == nil {
		return nil, nil
	}
	return json.Marshal(value)
}

func nullableFromPtr[T any](value *T) nullable.Nullable[T] {
	if value == nil {
		return nullable.NewNullNullable[T]()
	}
	return nullable.NewNullableWithValue(*value)
}

func nullableFromValue[T any](value T) nullable.Nullable[T] {
	return nullable.NewNullableWithValue(value)
}

func nullableInt32FromIntPtr(value *int) nullable.Nullable[int32] {
	if value == nil {
		return nullable.NewNullNullable[int32]()
	}
	return nullable.NewNullableWithValue(int32(*value))
}

func intPtrFromInt32(value *int32) *int {
	if value == nil {
		return nil
	}
	converted := int(*value)
	return &converted
}

func nullableIntPatchFromInt32(value nullable.Nullable[int32]) patch.NullableInt {
	result := patch.NullableInt{Set: value.IsSpecified()}
	if !result.Set || value.IsNull() {
		return result
	}
	converted := int(value.MustGet())
	result.Value = &converted
	return result
}

func nullableBoolPatchFromBool(value nullable.Nullable[bool]) patch.NullableBool {
	result := patch.NullableBool{Set: value.IsSpecified()}
	if !result.Set || value.IsNull() {
		return result
	}
	converted := value.MustGet()
	result.Value = &converted
	return result
}

func stringPatchFromNullable[T ~string](value nullable.Nullable[T]) *string {
	if !value.IsSpecified() {
		return nil
	}
	converted := ""
	if !value.IsNull() {
		converted = string(value.MustGet())
	}
	return &converted
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func agentInputCommandAPIError(err error) apierror.ResponseError {
	return apierror.ProjectScoped(err)
}
