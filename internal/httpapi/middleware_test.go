package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"

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
