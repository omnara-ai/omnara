package executionstore

import (
	"encoding/json"
	"time"
)

func sqlcTextFromEmpty(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func sqlcIDFromNil(value ID) *ID {
	if isNilID(value) {
		return nil
	}
	return &value
}

func idFromSQLCPtr(value *ID) ID {
	if value == nil {
		return NilID
	}
	return *value
}

func int64FromSQLCPtr(value *int64) int64 {
	if value == nil {
		return 0
	}
	return *value
}

func nullableTimeToZero(value *time.Time) time.Time {
	if value == nil {
		return time.Time{}
	}
	return *value
}

func canonicalSourceTime(value time.Time) time.Time {
	return time.UnixMicro(value.UnixMicro()).UTC()
}

func timePtr(value time.Time) *time.Time {
	return &value
}

func rawMessageFromSQLCPtr(value *json.RawMessage) json.RawMessage {
	if value == nil {
		return nil
	}
	return *value
}

func sqlcRawMessageFromEmpty(value json.RawMessage) *json.RawMessage {
	if len(value) == 0 {
		return nil
	}
	return &value
}

func sqlcInt32Ptr(value *int) *int32 {
	if value == nil {
		return nil
	}
	v := int32(*value)
	return &v
}

func intPtrFromSQLC(value *int32) *int {
	if value == nil {
		return nil
	}
	v := int(*value)
	return &v
}

func sameIntPtr(left, right *int) bool {
	if left == nil || right == nil {
		return left == right
	}
	return *left == *right
}

func stringFromSQLCText(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func sqlcStrings[T ~string](values []T) []string {
	if values == nil {
		return nil
	}
	out := make([]string, len(values))
	for index, value := range values {
		out[index] = string(value)
	}
	return out
}
