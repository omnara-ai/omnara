package httpapi

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/daemonprotocol"
	httpauth "github.com/omnara-ai/omnara/internal/httpapi/auth"
	"github.com/omnara-ai/omnara/internal/integration/discord"
	"github.com/omnara-ai/omnara/internal/integration/slack"
	"github.com/omnara-ai/omnara/internal/skills"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/stretchr/testify/require"
)

func TestRequiresAuthCoversAllAPIRoutes(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{path: "/api/v1/daemon/skills/skl_abc/archive", want: true},
		{path: "/api/v1/daemon/runtimes/abc/socket", want: true},
		{path: "/api/v1/orgs/abc/skills/skl_abc/archive", want: true},
		{path: "/api/v1/orgs", want: true},
		{path: "/mcp", want: true},
		{path: "/healthz", want: false},
		{path: "/api/auth/logout", want: true},
	}
	for _, tc := range cases {
		if got := requiresAuth(tc.path); got != tc.want {
			t.Errorf("requiresAuth(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}

func TestRequestBodyLimitSkillUploadRoutes(t *testing.T) {
	want := maxSkillUploadRequestBodyBytes
	if want <= maxRequestBodyBytes {
		t.Fatalf("sanity: skill upload cap (%d) should exceed the global default (%d)", want, maxRequestBodyBytes)
	}
	if want < int64(skills.MaxArchiveBytes) {
		t.Fatalf("sanity: skill upload cap (%d) must cover the archive size (%d)", want, skills.MaxArchiveBytes)
	}
	cases := []struct {
		name   string
		method string
		path   string
		want   int64
	}{
		{name: "skill upload", method: http.MethodPost, path: "/v1/orgs/o/skills", want: want},
		{
			name:   "skill update",
			method: http.MethodPost,
			path:   "/api/v1/orgs/o/skills/skl_abc",
			want:   want,
		},
		{
			name:   "skill grants use the default POST limit",
			method: http.MethodPost,
			path:   "/api/v1/orgs/o/skills/skl_abc/grants",
			want:   maxRequestBodyBytes,
		},
		{
			name:   "skill list is the default GET path",
			method: http.MethodGet,
			path:   "/api/v1/orgs/o/skills",
			want:   maxRequestBodyBytes,
		},
		{
			name:   "skill delete uses the default DELETE limit",
			method: http.MethodDelete,
			path:   "/api/v1/orgs/o/skills/skl_abc",
			want:   maxRequestBodyBytes,
		},
		{
			name:   "inputs still gets the attachment cap",
			method: http.MethodPost,
			path:   "/api/v1/orgs/o/projects/p/agents/a/inputs",
			want:   maxAttachmentRequestBodyBytes,
		},
		{
			name:   "unrelated POST still capped to default",
			method: http.MethodPost,
			path:   "/api/v1/orgs",
			want:   maxRequestBodyBytes,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, nil)
			if got := requestBodyLimit(req); got != tc.want {
				t.Errorf("requestBodyLimit(%s %s) = %d, want %d", tc.method, tc.path, got, tc.want)
			}
		})
	}
}

func TestRequestBodyLimitDaemonFileUpload(t *testing.T) {
	req := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/daemon/processes/prc_abc/file?filename=shot.png",
		nil,
	)
	if got := requestBodyLimit(req); got != daemonprotocol.MaxFileTransferBytes {
		t.Fatalf("file upload body limit = %d, want %d", got, daemonprotocol.MaxFileTransferBytes)
	}
}

func TestServerRejectsCanceledRequestsBeforeAuth(t *testing.T) {
	server := mustNewUnitServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	recorder := httptest.NewRecorder()
	server.Handler().ServeHTTP(recorder, httptest.NewRequestWithContext(ctx, http.MethodGet, "/api/v1/me", nil))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusServiceUnavailable)
	}
	if got := recorder.Header().Get("Connection"); got != "close" {
		t.Fatalf("Connection = %q, want close", got)
	}
}

func TestRejectCanceledRequestsLogsCancellationAtInfo(t *testing.T) {
	buf, log := newRequestEventCapture()
	next := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("canceled request reached the next handler")
	})
	handler := requestEvents(log, nil, nil)(rejectCanceledRequests(next))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequestWithContext(ctx, http.MethodGet, "/api/v1/me", nil))
	event := decodeRequestEvent(t, buf)
	for key, want := range map[string]any{
		"level":                      "info",
		"http.status_code":           float64(http.StatusServiceUnavailable),
		"http.request.cancel_cause":  context.Canceled.Error(),
		"http.request.cancel_source": "request_context",
	} {
		if got := event[key]; got != want {
			t.Fatalf("%s=%v want=%v in event %+v", key, got, want, event)
		}
	}
}

func TestRequestBodyLimitProviderRoutes(t *testing.T) {
	for path, want := range map[string]int64{
		httpauth.OAuthDeviceAuthorizationPath:        httpauth.MaxOAuthFormBytes,
		httpauth.OAuthTokenPath:                      httpauth.MaxOAuthFormBytes,
		slackEventsPath:                              slack.EventBodyMaxBytes,
		slackActionsPath:                             slack.ActionBodyMaxBytes,
		GitHubEventsPath:                             integrationstore.IntegrationInboxMaxPayloadBytes,
		"/api/integrations/discord/100/interactions": discord.InteractionMaxBytes,
	} {
		if got := requestBodyLimit(httptest.NewRequest(http.MethodPost, path, nil)); got != want {
			t.Fatalf("%s body limit = %d, want %d", path, got, want)
		}
	}
}

func TestMaxBodyBoundsUploadTime(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(maxBody(
		func(*http.Request) int64 { return 1024 },
		func(int64) time.Duration { return 100 * time.Millisecond },
	)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/reject" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if _, err := io.ReadAll(r.Body); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		select {
		case <-r.Context().Done():
		case <-time.After(200 * time.Millisecond):
		}
		_, _ = fmt.Fprintf(w, "canceled=%t", r.Context().Err() != nil)
	})))
	t.Cleanup(server.Close)
	for _, tc := range []struct {
		name      string
		request   string
		status    int
		keepAlive bool
	}{
		{
			name:    "upload finished before its deadline",
			request: "POST /read HTTP/1.1\r\nHost: x\r\nContent-Length: 5\r\n\r\n01234",
			status:  http.StatusOK, keepAlive: true,
		},
		{
			name:    "request without a body",
			request: "GET /read HTTP/1.1\r\nHost: x\r\n\r\n",
			status:  http.StatusOK, keepAlive: true,
		},
		{
			name:    "stalled upload",
			request: "POST /read HTTP/1.1\r\nHost: x\r\nContent-Length: 10\r\n\r\n01234",
			status:  http.StatusBadRequest,
		},
		{
			name:    "stalled chunked upload",
			request: "POST /read HTTP/1.1\r\nHost: x\r\nTransfer-Encoding: chunked\r\n\r\n5\r\n01234\r\n",
			status:  http.StatusBadRequest,
		},
		{
			name:    "early rejection with an unread body",
			request: "POST /reject HTTP/1.1\r\nHost: x\r\nContent-Length: 10\r\n\r\n01234",
			status:  http.StatusUnauthorized,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			conn, err := net.Dial("tcp", server.Listener.Addr().String())
			require.NoError(t, err)
			t.Cleanup(func() { _ = conn.Close() })
			require.NoError(t, conn.SetDeadline(time.Now().Add(3*time.Second)))
			reader := bufio.NewReader(conn)
			_, err = io.WriteString(conn, tc.request)
			require.NoError(t, err)
			response, err := http.ReadResponse(reader, nil)
			require.NoError(t, err)
			body, err := io.ReadAll(response.Body)
			require.NoError(t, err)
			require.Equal(t, tc.status, response.StatusCode)
			require.Equal(t, !tc.keepAlive, response.Close)
			if !tc.keepAlive {
				_, err = reader.ReadByte()
				require.ErrorIs(t, err, io.EOF, "the server must stop waiting for the upload and close the connection")
				return
			}
			require.Equal(t, "canceled=false", string(body), "the upload deadline must not outlive the body")
			_, err = io.WriteString(conn, "GET /read HTTP/1.1\r\nHost: x\r\n\r\n")
			require.NoError(t, err)
			next, err := http.ReadResponse(reader, nil)
			require.NoError(t, err)
			nextBody, err := io.ReadAll(next.Body)
			require.NoError(t, err)
			require.Equal(t, "canceled=false", string(nextBody))
		})
	}
}
