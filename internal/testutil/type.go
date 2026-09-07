// Package testutil provides small operations for test prerequisites.
package testutil

import (
	"reflect"
	"testing"
)

// RequireType extracts a value using Go's type assertion semantics and reports
// a failed prerequisite at the caller. It does not require non-nil or nonempty
// values. Like t.Fatal, it must run in the goroutine running the test.
func RequireType[T any](t testing.TB, value any) T {
	t.Helper()
	typed, ok := value.(T)
	if !ok {
		t.Fatalf("value has type %T, want %v; value = %#v", value, reflect.TypeFor[T](), value)
	}
	return typed
}
