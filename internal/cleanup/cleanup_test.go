package cleanup

import (
	"context"
	"testing"
	"time"
)

func TestDetachOutlivesCancellationUntilLifetimeEnds(t *testing.T) {
	lifetime, endLifetime := context.WithCancel(t.Context())
	defer endLifetime()
	parent, cancelParent := context.WithCancel(WithLifetime(t.Context(), lifetime))
	cancelParent()

	detached, cancel := Detach(parent, time.Hour)
	defer cancel()
	if detached.Err() != nil {
		t.Fatal("detached context ended with its canceled parent")
	}
	if _, bounded := detached.Deadline(); !bounded {
		t.Fatal("detached context has no timeout")
	}
	nested, cancelNested := Detach(detached, time.Hour)
	defer cancelNested()

	endLifetime()
	for _, ctx := range []context.Context{detached, nested} {
		select {
		case <-ctx.Done():
		case <-time.After(time.Second):
			t.Fatal("detached context outlived the cleanup lifetime")
		}
	}
}
