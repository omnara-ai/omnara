package testutil

import (
	"reflect"
	"testing"
)

func RequireType[T any](t testing.TB, value any) T {
	t.Helper()
	typed, ok := value.(T)
	if !ok {
		t.Fatalf("value has type %T, want %v; value = %#v", value, reflect.TypeFor[T](), value)
	}
	return typed
}
