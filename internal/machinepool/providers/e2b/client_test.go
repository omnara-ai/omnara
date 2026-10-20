package e2b

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/omnara-ai/omnara/internal/machinepool/providers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testRESTClient(t *testing.T, handler http.HandlerFunc) *restClient {
	t.Helper()
	server := httptest.NewTLSServer(handler)
	t.Cleanup(server.Close)
	base, ok := server.Client().Transport.(*http.Transport)
	require.True(t, ok)
	transport := base.Clone()
	transport.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, server.Listener.Addr().String())
	}
	transport.TLSClientConfig.InsecureSkipVerify = true
	return newRESTClient("e2b.test", "api-key", &http.Client{Transport: transport})
}

func connectFrame(flags byte, payload string) []byte {
	frame := make([]byte, 5, 5+len(payload))
	frame[0] = flags
	binary.BigEndian.PutUint32(frame[1:], uint32(len(payload)))
	return append(frame, payload...)
}

func TestRESTClientUsesE2BEndpoints(t *testing.T) {
	var startBody startProcessRequest
	client := testRESTClient(t, func(w http.ResponseWriter, r *http.Request) {
		route := r.Method + " " + r.Host + r.URL.Path
		if strings.HasPrefix(r.Host, "49983-") {
			assert.Equal(t, "envd-token", r.Header.Get("X-Access-Token"), route)
		} else {
			assert.Equal(t, "api-key", r.Header.Get("X-Api-Key"), route)
		}
		switch route {
		case "GET dashboard-api.e2b.test/templates":
			assert.Equal(t, "claude", r.URL.Query().Get("search"))
			assert.Equal(t, "100", r.URL.Query().Get("limit"))
			if r.URL.Query().Get("cursor") == "" {
				_, _ = w.Write([]byte(`{"data":[{"templateID":"t1","aliases":["claude"],"names":["claude"],` +
					`"cpuCount":2,"memoryMB":2048,"isDefault":true}],"nextCursor":"next"}`))
				return
			}
			assert.Equal(t, "next", r.URL.Query().Get("cursor"))
			_, _ = w.Write([]byte(`{"data":[],"nextCursor":null}`))
		case "POST api.e2b.test/v2/sandboxes":
			var body map[string]any
			assert.NoError(t, json.NewDecoder(r.Body).Decode(&body))
			assert.Equal(t, map[string]any{
				"templateID": "claude",
				"timeout":    float64(300),
				"metadata":   map[string]any{"omnara-machine": "mch_x"},
				"network":    map[string]any{"allowPublicTraffic": false},
			}, body)
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"sandboxID":"sbx1","envdAccessToken":"envd-token"}`))
		case "GET api.e2b.test/v2/sandboxes":
			assert.Equal(t, "running,paused", r.URL.Query().Get("state"))
			assert.Equal(t, "2", r.URL.Query().Get("limit"))
			assert.Equal(t, "omnara-machine=mch_x", r.URL.Query().Get("metadata"))
			_, _ = w.Write([]byte(`[{"sandboxID":"sbx1","state":"running","metadata":{"omnara-machine":"mch_x"}}]`))
		case "GET api.e2b.test/sandboxes/sbx1":
			_, _ = w.Write([]byte(`{"sandboxID":"sbx1","state":"running","cpuCount":2,"memoryMB":2048,` +
				`"envdAccessToken":"envd-token"}`))
		case "GET api.e2b.test/sandboxes/gone", "DELETE api.e2b.test/sandboxes/gone":
			w.WriteHeader(http.StatusNotFound)
		case "DELETE api.e2b.test/sandboxes/sbx1":
			w.WriteHeader(http.StatusNoContent)
		case "POST api.e2b.test/sandboxes/sbx1/timeout":
			var body map[string]int
			assert.NoError(t, json.NewDecoder(r.Body).Decode(&body))
			assert.Equal(t, map[string]int{"timeout": maxTimeoutSeconds}, body)
			w.WriteHeader(http.StatusNoContent)
		case "POST 49983-sbx1.e2b.test/process.Process/List":
			_, _ = w.Write([]byte(`{"processes":[{"pid":7,"tag":"omnara-daemon","config":{"cmd":"/bin/sh"}}]}`))
		case "POST 49983-sbx1.e2b.test/process.Process/Start":
			assert.Equal(t, "application/connect+json", r.Header.Get("Content-Type"))
			raw, err := io.ReadAll(r.Body)
			assert.NoError(t, err)
			assert.Equal(t, uint32(len(raw)-5), binary.BigEndian.Uint32(raw[1:5]))
			assert.NoError(t, json.Unmarshal(raw[5:], &startBody))
			_, _ = w.Write(connectFrame(0, `{"event":{"start":{"pid":42}}}`))
		default:
			t.Errorf("unexpected request %s", route)
			w.WriteHeader(http.StatusTeapot)
		}
	})
	ctx := t.Context()

	page, next, err := client.ListTemplates(ctx, "claude", "")
	require.NoError(t, err)
	require.Equal(t, "next", next)
	require.Equal(t, []template{{TemplateID: "t1", Aliases: []string{"claude"}, Names: []string{"claude"},
		CPUCount: 2, MemoryMB: 2048, IsDefault: true}}, page)
	_, next, err = client.ListTemplates(ctx, "claude", "next")
	require.NoError(t, err)
	require.Empty(t, next)

	created, err := client.CreateSandbox(ctx, createSandboxRequest{
		TemplateID: "claude",
		Timeout:    createTimeoutSeconds,
		Metadata:   map[string]string{"omnara-machine": "mch_x"},
	})
	require.NoError(t, err)
	require.Equal(t, "sbx1", created.SandboxID)
	found, err := client.FindSandboxes(ctx, map[string]string{"omnara-machine": "mch_x"})
	require.NoError(t, err)
	require.Len(t, found, 1)
	target, ok, err := client.GetSandbox(ctx, "sbx1")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "envd-token", target.EnvdAccessToken)
	_, ok, err = client.GetSandbox(ctx, "gone")
	require.NoError(t, err)
	require.False(t, ok)
	require.NoError(t, client.ExtendTimeout(ctx, "sbx1", maxTimeoutSeconds))

	processes, err := client.ListProcesses(ctx, target)
	require.NoError(t, err)
	require.Equal(t, []process{{Tag: daemonProcessTag}}, processes)
	require.NoError(t, client.StartProcess(ctx, target, startProcessRequest{
		Process: processConfig{Cmd: "/bin/sh", Args: []string{"-c", "true"}, Envs: map[string]string{"A": "b"}},
		Tag:     daemonProcessTag,
	}))
	require.Equal(t, daemonProcessTag, startBody.Tag)
	require.False(t, startBody.Stdin)
	require.Equal(t, map[string]string{"A": "b"}, startBody.Process.Envs)

	require.NoError(t, client.DeleteSandbox(ctx, "sbx1"))
	require.NoError(t, client.DeleteSandbox(ctx, "gone"))
}

func TestRESTClientUsesTheSandboxDomainForEnvd(t *testing.T) {
	var host string
	client := testRESTClient(t, func(w http.ResponseWriter, r *http.Request) {
		host = r.Host
		_, _ = w.Write([]byte(`{"processes":[]}`))
	})
	_, err := client.ListProcesses(t.Context(), sandbox{SandboxID: "sbx1", Domain: "byoc.example.com"})
	require.NoError(t, err)
	require.Equal(t, "49983-sbx1.byoc.example.com", host)
}

func TestRESTClientDoesNotExposeErrorBodies(t *testing.T) {
	client := testRESTClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "7")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"message":"secret upstream detail"}`))
	})
	_, err := client.CreateSandbox(t.Context(), createSandboxRequest{TemplateID: "claude"})
	require.Error(t, err)
	require.NotContains(t, err.Error(), "secret")
	require.Equal(t, http.StatusTooManyRequests, apiStatusCode(err))
	delay, ok := providers.RetryAfter(err)
	require.True(t, ok)
	require.Equal(t, "7s", delay.String())
	err = client.StartProcess(t.Context(), sandbox{SandboxID: "sbx1"}, startProcessRequest{})
	require.Equal(t, http.StatusTooManyRequests, apiStatusCode(err))
}

func TestReadStartEvent(t *testing.T) {
	start := connectFrame(0, `{"event":{"start":{"pid":42}}}`)
	oversized := make([]byte, 5)
	binary.BigEndian.PutUint32(oversized[1:], maxStartFrameBytes+1)
	for _, test := range []struct {
		name   string
		stream []byte
		want   string
	}{
		{name: "start", stream: start},
		{name: "data then start", stream: append(connectFrame(0, `{"event":{"data":{"stdout":"aGk="}}}`), start...)},
		{name: "end stream first", stream: connectFrame(connectEndStreamFlag, `{"error":{"code":"internal"}}`),
			want: "ended before the process started"},
		{name: "eof", stream: nil, want: "ended before the process started"},
		{name: "truncated header", stream: start[:3], want: "ended before the process started"},
		{name: "truncated payload", stream: start[:len(start)-2], want: "truncated"},
		{name: "oversized", stream: oversized, want: "exceeds"},
		{name: "not json", stream: connectFrame(0, `nope`), want: "decode"},
	} {
		err := readStartEvent(bytes.NewReader(test.stream))
		if test.want == "" {
			require.NoError(t, err, test.name)
			continue
		}
		require.ErrorContains(t, err, test.want, test.name)
	}
}
