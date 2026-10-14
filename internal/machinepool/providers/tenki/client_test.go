package tenki

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/machinepool/providers"
	"github.com/stretchr/testify/require"
)

func TestTenkiRESTClientSessionLifecycle(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.Header.Get("Authorization") != "Bearer tk_test" ||
			r.Header.Get("Content-Type") != "application/json" ||
			r.Header.Get("Connect-Protocol-Version") != "1" {
			t.Errorf("unexpected request %s %s %+v", r.Method, r.URL.Path, r.Header)
			return
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode body: %v", err)
			return
		}
		switch r.URL.Path {
		case servicePath + "CreateSession":
			runtime, _ := body["runtime"].(map[string]any)
			if body["allowInbound"] != false || body["sticky"] != true ||
				runtime["restartPolicy"] != "TEMPLATE_RESTART_POLICY_NEVER" {
				t.Errorf("create body = %+v", body)
			}
			_, _ = w.Write([]byte(`{"session":{"id":"s1","state":"SESSION_STATE_CREATING","sticky":true}}`))
		case servicePath + "GetSession", servicePath + "TerminateSession":
			if body["sessionId"] == "missing" {
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"code":"not_found","message":"session not found"}`))
				return
			}
			_, _ = w.Write([]byte(`{"session":{"id":"s1","state":"SESSION_STATE_RUNNING"}}`))
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()
	client := newRESTClient(server.URL, "tk_test", server.Client())

	created, err := client.Create(t.Context(), createRequest{
		Sticky:  true,
		Runtime: bootRuntime{RestartPolicy: "TEMPLATE_RESTART_POLICY_NEVER"},
	})
	require.NoError(t, err)
	require.Equal(t, session{ID: "s1", State: "CREATING", Sticky: true}, created)
	current, found, err := client.Get(t.Context(), "s1")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "RUNNING", current.State)
	_, found, err = client.Get(t.Context(), "missing")
	require.NoError(t, err)
	require.False(t, found)
	require.NoError(t, client.Delete(t.Context(), "s1"))
	require.NoError(t, client.Delete(t.Context(), "missing"))
}

func TestTenkiRESTClientListsAllPages(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body listRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode body: %v", err)
			return
		}
		if body.PageSize != listPageSize || len(body.Tags) != 1 || body.Tags[0] != managedTag {
			t.Errorf("list body = %+v", body)
		}
		switch body.PageToken {
		case "":
			_, _ = w.Write([]byte(`{"sessions":[{"id":"a","state":"SESSION_STATE_RUNNING"}],"nextPageToken":"p2"}`))
		case "p2":
			_, _ = w.Write([]byte(`{"sessions":[{"id":"b","state":"SESSION_STATE_TERMINATED"}]}`))
		}
	}))
	defer server.Close()

	sessions, err := newRESTClient(server.URL, "tk_test", server.Client()).List(t.Context())
	require.NoError(t, err)
	require.Equal(t, []session{{ID: "a", State: "RUNNING"}, {ID: "b", State: "TERMINATED"}}, sessions)
}

func TestTenkiRESTClientRejectsRepeatedPageToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"sessions":[],"nextPageToken":"same"}`))
	}))
	defer server.Close()

	_, err := newRESTClient(server.URL, "tk_test", server.Client()).List(t.Context())
	require.ErrorContains(t, err, "repeated a page token")
}

func TestTenkiRESTClientDoesNotExposeErrorResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "7")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"code":"resource_exhausted","message":"OMNARA_MACHINE_TOKEN=secret-token"}`))
	}))
	defer server.Close()

	_, _, err := newRESTClient(server.URL, "tk_test", server.Client()).Get(t.Context(), "s1")
	require.EqualError(t, err, "tenki API returned HTTP 429 (resource_exhausted)")
	delay, ok := providers.RetryAfter(err)
	require.True(t, ok)
	require.Equal(t, 7*time.Second, delay)
	require.True(t, rejectedBeforeCreate(err))
}
