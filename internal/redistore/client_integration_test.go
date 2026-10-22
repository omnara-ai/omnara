//go:build integration

package redistore_test

import (
	"context"
	"errors"
	"io"
	"net"
	"net/url"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/redistore"
	"github.com/omnara-ai/omnara/internal/testutil/integrationredis"
)

type heldRedisResponse struct {
	io.Reader
	hold    *atomic.Bool
	started chan<- struct{}
	release <-chan struct{}
}

func (r heldRedisResponse) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	if n > 0 && r.hold.Load() {
		select {
		case r.started <- struct{}{}:
		default:
		}
		<-r.release
	}
	return n, err
}

func TestRedisCommandDeadlineBoundsNetworkIO(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	upstreamURL, err := url.Parse(integrationredis.URL(t))
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var hold atomic.Bool
	started, release := make(chan struct{}, 1), make(chan struct{})
	releaseResponse := sync.OnceFunc(func() { close(release) })
	var connections sync.WaitGroup
	acceptDone := make(chan struct{})
	go func() {
		defer close(acceptDone)
		for {
			downstream, err := listener.Accept()
			if err != nil {
				return
			}
			connections.Go(func() {
				defer downstream.Close()
				var dialer net.Dialer
				upstream, err := dialer.DialContext(ctx, "tcp", upstreamURL.Host)
				if err != nil {
					return
				}
				defer upstream.Close()
				responseDone := make(chan struct{})
				go func() {
					defer close(responseDone)
					_, _ = io.Copy(downstream, heldRedisResponse{upstream, &hold, started, release})
				}()
				_, _ = io.Copy(upstream, downstream)
				_ = upstream.Close()
				_ = downstream.Close()
				<-responseDone
			})
		}
	}()
	t.Cleanup(func() {
		releaseResponse()
		_ = listener.Close()
		<-acceptDone
		connections.Wait()
	})
	proxyURL := *upstreamURL
	proxyURL.Host = listener.Addr().String()
	query := proxyURL.Query()
	query.Set("read_timeout", "30s")
	query.Set("write_timeout", "30s")
	proxyURL.RawQuery = query.Encode()
	client, err := redistore.Connect(proxyURL.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	hold.Store(true)
	commandCtx, commandCancel := context.WithTimeout(ctx, 2*time.Second)
	defer commandCancel()
	result := make(chan error, 1)
	go func() { result <- client.Ping(commandCtx) }()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("Redis command did not reach the response barrier")
	}
	select {
	case err := <-result:
		var timeout net.Error
		if !errors.Is(err, context.DeadlineExceeded) && !(errors.As(err, &timeout) && timeout.Timeout()) {
			t.Fatalf("blocked Redis command returned %v, want a deadline error", err)
		}
	case <-ctx.Done():
		t.Fatal("command ignored its deadline and waited for the Redis socket timeout")
	}
	releaseResponse()
	if err := client.Ping(ctx); err != nil {
		t.Fatalf("Redis client did not recover after command deadline: %v", err)
	}
}

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
