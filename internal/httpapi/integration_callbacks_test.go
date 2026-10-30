package httpapi

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/integration/slack"
	"github.com/stretchr/testify/require"
)

func rejectionTestHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/next":
			_, _ = fmt.Fprintf(w, "canceled=%t", r.Context().Err() != nil)
			return
		case "/canceled":
			ctx, cancel := context.WithCancel(r.Context())
			cancel()
			rejectCanceledRequests(http.NotFoundHandler()).ServeHTTP(w, r.WithContext(ctx))
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 100*time.Millisecond)
		defer cancel()
		if _, ok := readIntegrationCallbackBody(w, r.WithContext(ctx), 16); ok {
			w.WriteHeader(http.StatusNoContent)
		}
	})
}

func TestRejectedRequestsCloseHTTP1Connection(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(rejectionTestHandler())
	t.Cleanup(server.Close)
	for _, tc := range []struct {
		name       string
		request    string
		closeWrite bool
		status     int
		keepAlive  bool
	}{
		{
			name:    "complete body",
			request: "POST /read HTTP/1.1\r\nHost: x\r\nContent-Length: 10\r\n\r\n0123456789",
			status:  http.StatusNoContent, keepAlive: true,
		},
		{
			name:    "body at limit",
			request: "POST /read HTTP/1.1\r\nHost: x\r\nContent-Length: 16\r\n\r\n" + strings.Repeat("x", 16),
			status:  http.StatusNoContent, keepAlive: true,
		},
		{
			name:    "stalled body",
			request: "POST /read HTTP/1.1\r\nHost: x\r\nContent-Length: 10\r\n\r\n01234",
			status:  http.StatusBadRequest,
		},
		{
			name:       "truncated body",
			request:    "POST /read HTTP/1.1\r\nHost: x\r\nContent-Length: 10\r\n\r\n01234",
			closeWrite: true,
			status:     http.StatusBadRequest,
		},
		{
			name:    "malformed chunked body",
			request: "POST /read HTTP/1.1\r\nHost: x\r\nTransfer-Encoding: chunked\r\n\r\nzz\r\nabc\r\n",
			status:  http.StatusBadRequest,
		},
		{
			name:    "oversized body",
			request: "POST /read HTTP/1.1\r\nHost: x\r\nContent-Length: 32\r\n\r\n" + strings.Repeat("x", 32),
			status:  http.StatusRequestEntityTooLarge,
		},
		{
			name:    "oversized unfinished body",
			request: "POST /read HTTP/1.1\r\nHost: x\r\nContent-Length: 40\r\n\r\n" + strings.Repeat("x", 25),
			status:  http.StatusRequestEntityTooLarge,
		},
		{
			name:    "canceled request with unfinished body",
			request: "POST /canceled HTTP/1.1\r\nHost: x\r\nContent-Length: 10\r\n\r\n01234",
			status:  http.StatusServiceUnavailable,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			conn, err := net.Dial("tcp", server.Listener.Addr().String())
			require.NoError(t, err)
			t.Cleanup(func() { _ = conn.Close() })
			require.NoError(t, conn.SetDeadline(time.Now().Add(3*time.Second)))
			_, err = io.WriteString(conn, tc.request)
			require.NoError(t, err)
			if tc.closeWrite {
				tcpConn, ok := conn.(*net.TCPConn)
				require.True(t, ok)
				require.NoError(t, tcpConn.CloseWrite())
			}
			reader := bufio.NewReader(conn)
			response, err := http.ReadResponse(reader, nil)
			require.NoError(t, err)
			_, _ = io.Copy(io.Discard, response.Body)
			require.Equal(t, tc.status, response.StatusCode)
			require.Equal(t, !tc.keepAlive, response.Close)
			if !tc.keepAlive {
				_, err = reader.ReadByte()
				require.ErrorIs(t, err, io.EOF, "the server must close a rejected connection without waiting for more bytes")
				return
			}
			_, err = io.WriteString(conn, "GET /next HTTP/1.1\r\nHost: x\r\n\r\n")
			require.NoError(t, err)
			next, err := http.ReadResponse(reader, nil)
			require.NoError(t, err)
			body, err := io.ReadAll(next.Body)
			require.NoError(t, err)
			require.Equal(t, "canceled=false", string(body))
		})
	}
}

func TestServerHandlerClosesOnlyRejectedWebhookConnections(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(mustNewUnitServer(t).Handler())
	t.Cleanup(server.Close)
	dial := func(t *testing.T) (net.Conn, *bufio.Reader) {
		t.Helper()
		conn, err := net.Dial("tcp", server.Listener.Addr().String())
		require.NoError(t, err)
		t.Cleanup(func() { _ = conn.Close() })
		require.NoError(t, conn.SetDeadline(time.Now().Add(5*time.Second)))
		return conn, bufio.NewReader(conn)
	}
	head := func(contentLength int) string {
		return fmt.Sprintf("POST %s HTTP/1.1\r\nHost: x\r\nContent-Type: application/json\r\nContent-Length: %d\r\n\r\n",
			slackEventsPath, contentLength)
	}

	t.Run("successful request keeps the connection", func(t *testing.T) {
		t.Parallel()
		conn, reader := dial(t)
		body := `{"type":"url_verification","challenge":"ok"}`
		for range 2 {
			_, err := io.WriteString(conn, head(len(body))+body)
			require.NoError(t, err)
			response, err := http.ReadResponse(reader, nil)
			require.NoError(t, err)
			_, _ = io.Copy(io.Discard, response.Body)
			require.Equal(t, http.StatusOK, response.StatusCode)
			require.False(t, response.Close)
		}
	})
	t.Run("oversized upload that stops early", func(t *testing.T) {
		t.Parallel()
		conn, reader := dial(t)
		_, err := io.WriteString(conn, head(slack.EventBodyMaxBytes+64*1024))
		require.NoError(t, err)
		_, err = conn.Write(bytes.Repeat([]byte(" "), slack.EventBodyMaxBytes+1))
		require.NoError(t, err)
		response, err := http.ReadResponse(reader, nil)
		require.NoError(t, err)
		_, _ = io.Copy(io.Discard, response.Body)
		require.Equal(t, http.StatusRequestEntityTooLarge, response.StatusCode)
		require.True(t, response.Close)
		_, err = reader.ReadByte()
		require.ErrorIs(t, err, io.EOF)
	})
}

func TestIntegrationCallbackBodyFailureKeepsHTTP2Connection(t *testing.T) {
	t.Parallel()
	var connections atomic.Int32
	server := httptest.NewUnstartedServer(rejectionTestHandler())
	server.EnableHTTP2 = true
	server.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			connections.Add(1)
		}
	}
	server.StartTLS()
	t.Cleanup(server.Close)
	client := server.Client()
	client.Timeout = 5 * time.Second

	body, writer := io.Pipe()
	t.Cleanup(func() { _ = writer.Close() })
	go func() { _, _ = io.WriteString(writer, "01234") }()
	request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, server.URL+"/read", body)
	require.NoError(t, err)
	response, err := client.Do(request)
	require.NoError(t, err)
	_, _ = io.Copy(io.Discard, response.Body)
	_ = response.Body.Close()
	require.Equal(t, 2, response.ProtoMajor)
	require.Equal(t, http.StatusBadRequest, response.StatusCode)

	next, err := client.Get(server.URL + "/next")
	require.NoError(t, err)
	nextBody, err := io.ReadAll(next.Body)
	_ = next.Body.Close()
	require.NoError(t, err)
	require.Equal(t, "canceled=false", string(nextBody))
	require.Equal(t, int32(1), connections.Load(), "a failed HTTP/2 stream must not close the connection")
}
