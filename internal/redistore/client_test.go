package redistore_test

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/redistore"
)

func TestNewWithTimeoutBoundsUnresponsiveRedis(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		var conns []net.Conn
		for {
			conn, err := listener.Accept()
			if err != nil {
				for _, conn := range conns {
					_ = conn.Close()
				}
				return
			}
			conns = append(conns, conn)
		}
	}()
	client, err := redistore.NewWithTimeout("redis://"+listener.Addr().String(), 50*time.Millisecond)
	if err != nil {
		t.Fatalf("create client: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	started := time.Now()
	if _, err := client.EvalInt(context.Background(), "return 1", nil); err == nil {
		t.Fatal("eval against an unresponsive server succeeded")
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("eval took %s, want it bounded by the client timeout", elapsed)
	}
}
