package machinedaemon

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFailureReportTruncation(t *testing.T) {
	detail := strings.Repeat("prefix", 10) + strings.Repeat("tail", MaxFailureDetailBytes/4)
	wantDetail := detail[len(detail)-MaxFailureDetailBytes:]
	tests := map[string]func(*Client) error{
		"runtime": func(client *Client) error {
			return client.ReportRuntimeFailure(context.Background(), detail, false)
		},
		"update": func(client *Client) error {
			return client.ReportUpdateFailure(context.Background(), UpdateFailureReport{
				DaemonVersion: "1.0.0",
				Detail:        detail,
			})
		},
		"uninstall": func(client *Client) error {
			return client.ReportUninstall(context.Background(), detail)
		},
	}
	for name, report := range tests {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if got := r.URL.Query().Get("capture_status"); got != "1" {
					t.Errorf("capture status = %q, want 1", got)
				}
				body, err := io.ReadAll(r.Body)
				if err != nil {
					t.Error(err)
				}
				if got := string(body); got != wantDetail {
					t.Errorf("failure detail length = %d, want %d", len(got), len(wantDetail))
				}
				w.WriteHeader(http.StatusNoContent)
			}))
			defer server.Close()
			client := New(Config{APIURL: server.URL}, server.Client(), nil)
			require.NoError(t, report(&client))
		})
	}
}

func TestRuntimeFailureReportPreservesCaptureTruncation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("capture_status") != "1" {
			t.Error("missing capture truncation flag")
		}
		body, err := io.ReadAll(r.Body)
		if err != nil || string(body) != "short tail" {
			t.Errorf("report body = %q, err = %v", body, err)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	client := New(Config{APIURL: server.URL}, server.Client(), nil)
	require.NoError(t, client.ReportRuntimeFailure(context.Background(), "short tail", true))
}
