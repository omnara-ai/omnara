package errutil

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

func TestOnlyMatches(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "direct", err: context.Canceled, want: true},
		{name: "wrapped", err: fmt.Errorf("query: %w", context.Canceled), want: true},
		{name: "joined", err: errors.Join(context.Canceled, context.Canceled), want: true},
		{name: "mixed", err: errors.Join(context.Canceled, errors.New("connection refused"))},
		{name: "unrelated", err: errors.New("connection refused")},
		{name: "nil"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := OnlyMatches(test.err, context.Canceled); got != test.want {
				t.Fatalf("OnlyMatches(%v, context.Canceled) = %t, want %t", test.err, got, test.want)
			}
		})
	}
}
