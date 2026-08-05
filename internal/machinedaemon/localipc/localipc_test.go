package localipc

import (
	"bufio"
	"context"
	"net"
	"path/filepath"
	"testing"
	"time"
)

func TestLocalIPCRoundTrip(t *testing.T) {

	endpoint := filepath.Join(t.TempDir(), "runner.sock")
	roundTripEndpoint(t, endpoint)
}

func TestLocalIPCRoundTripLongEndpoint(t *testing.T) {

	endpoint := filepath.Join(
		t.TempDir(),
		"this",
		"is",
		"a",
		"very",
		"long",
		"endpoint",
		"path",
		"that",
		"exceeds",
		"the",
		"usual",
		"unix",
		"domain",
		"socket",
		"limit",
		"runner.sock",
	)
	roundTripEndpoint(t, endpoint)
}

func roundTripEndpoint(t *testing.T, endpoint string) {
	t.Helper()
	listener, err := Listen(context.Background(), endpoint)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer listener.Close()
	t.Cleanup(func() { _ = Cleanup(endpoint) })
	done := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			done <- err
			return
		}
		defer conn.Close()
		line, err := bufio.NewReader(conn).ReadString('\n')
		if err != nil {
			done <- err
			return
		}
		if line != "ping\n" {
			done <- &net.OpError{Op: "read", Err: unexpectedLineError(line)}
			return
		}
		_, err = conn.Write([]byte("pong\n"))
		done <- err
	}()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	conn, err := Dial(ctx, endpoint)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	if _, err := conn.Write([]byte("ping\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	line, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if line != "pong\n" {
		t.Fatalf("line = %q, want pong", line)
	}
	if err := conn.Close(); err != nil {
		t.Fatalf("close conn: %v", err)
	}
	if err := <-done; err != nil {
		t.Fatalf("server: %v", err)
	}
}

type unexpectedLineError string

func (e unexpectedLineError) Error() string {
	return "unexpected line: " + string(e)
}
