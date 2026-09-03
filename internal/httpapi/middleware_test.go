package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/omnara-ai/omnara/internal/daemonprotocol"
	"github.com/omnara-ai/omnara/internal/skills"
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
			name:   "channel webhook gets the attachment cap",
			method: http.MethodPost,
			path:   "/api/v1/channel-connector/apps/iapp_abc/events",
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

func TestRequestBodyLimitDaemonArtifactUpload(t *testing.T) {
	req := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/daemon/tool-calls/tcl_abc/artifact?filename=shot.png",
		nil,
	)
	if got := requestBodyLimit(req); got != daemonprotocol.MaxArtifactUploadBytes {
		t.Fatalf("artifact upload body limit = %d, want %d", got, daemonprotocol.MaxArtifactUploadBytes)
	}
}

func TestChannelConnectorResponsesAreNeverCacheable(t *testing.T) {
	handler := channelConnectorNoStore(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	for _, path := range []string{
		"/api/v1/channel-connector/apps/iapp_test/configuration",
		"/api/v1/channel-connector/deliveries/claim",
	} {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if got := response.Header().Get("Cache-Control"); got != "no-store" {
			t.Fatalf("GET %s Cache-Control = %q, want no-store", path, got)
		}
	}
}
