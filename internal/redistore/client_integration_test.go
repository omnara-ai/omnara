//go:build integration

package redistore_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/testutil/integrationredis"
)

func TestSubscriptionContinuesAfterHandlerPanic(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	client := integrationredis.OpenClient(t)
	channel := "omnara:test:subscription-panic:" + uuid.NewString()
	var calls atomic.Int32
	first := make(chan struct{})
	second := make(chan struct{})
	sub, err := client.Subscribe(
		ctx,
		channel,
		nil,
		func(context.Context, string, []byte) {
			switch calls.Add(1) {
			case 1:
				close(first)
				panic("subscriber failure")
			case 2:
				close(second)
			}
		},
	)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	t.Cleanup(func() { _ = sub.Unsubscribe() })

	if err := client.Publish(ctx, channel, []byte("first")); err != nil {
		t.Fatalf("publish first message: %v", err)
	}
	select {
	case <-first:
	case <-ctx.Done():
		t.Fatalf("wait for first message: %v", ctx.Err())
	}
	if err := client.Publish(ctx, channel, []byte("second")); err != nil {
		t.Fatalf("publish second message: %v", err)
	}
	select {
	case <-second:
	case <-ctx.Done():
		t.Fatalf("subscription stopped after handler panic: %v", ctx.Err())
	}
}

func TestSubscriptionUnsubscribeAfterContextCancelAndClientCloseIsIdempotent(t *testing.T) {
	parentCtx, cancelParent := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelParent()

	client := integrationredis.OpenClient(t)
	subCtx, cancelSubCtx := context.WithCancel(parentCtx)
	sub, err := client.Subscribe(
		subCtx,
		"omnara:test:subscription:"+uuid.NewString(),
		nil,
		func(context.Context, string, []byte) {},
	)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	cancelSubCtx()
	if err := client.Close(); err != nil {
		t.Fatalf("close client: %v", err)
	}

	if err := sub.Unsubscribe(); err != nil {
		t.Fatalf("unsubscribe after context cancel and client close: %v", err)
	}
	if err := sub.Unsubscribe(); err != nil {
		t.Fatalf("second unsubscribe: %v", err)
	}
}
