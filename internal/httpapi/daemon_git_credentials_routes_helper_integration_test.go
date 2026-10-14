//go:build integration

package httpapi

import (
	"bytes"
	"net/http"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/machinedaemon"
	"github.com/omnara-ai/omnara/internal/machinedaemon/localipc"
	"github.com/stretchr/testify/require"
)

func TestDaemonGitCredentialsPermanentFailureStopsHelperRetries(t *testing.T) {
	t.Setenv("OMNARA_GIT_CREDENTIAL_CAPABILITY", "test-process-capability")
	for _, failure := range []string{"missing Contents", "permanent provider failure"} {
		t.Run(failure, func(t *testing.T) {
			f := newDaemonGitCredentialsFixture(t)
			f.accept(t, f.process)
			if failure == "missing Contents" {
				f.contentsPermission = ""
			} else {
				f.providerStatus = http.StatusUnauthorized
			}
			endpoint := filepath.Join(t.TempDir(), "credentials.sock")
			listener, err := localipc.Listen(t.Context(), endpoint)
			require.NoError(t, err)
			var requests atomic.Int32
			server := &http.Server{ReadHeaderTimeout: time.Second, Handler: http.HandlerFunc(
				func(w http.ResponseWriter, r *http.Request) {
					requests.Add(1)
					r.URL.Path = "/api/v1/daemon/processes/" + f.process.ProcessID + "/git-credentials"
					r.Header.Set("Authorization", "Bearer "+f.process.Token)
					f.handler.ServeHTTP(w, r)
				},
			)}
			done := make(chan error, 1)
			go func() { done <- server.Serve(listener) }()
			t.Cleanup(func() {
				require.NoError(t, server.Close())
				require.ErrorIs(t, <-done, http.ErrServerClosed)
				require.NoError(t, localipc.Cleanup(endpoint))
			})
			var output bytes.Buffer
			err = machinedaemon.RunGitCredentialHelper(t.Context(), endpoint, f.process.ProcessID, "get",
				strings.NewReader("protocol=https\nhost=github.com\n\n"), &output)
			require.Error(t, err)
			require.Equal(t, "quit=true\n\n", output.String())
			require.EqualValues(t, 1, requests.Load(), "a permanent HTTP failure must stop the helper's retry loop")
			require.EqualValues(t, 1, f.minted.Load(), "the helper must not mint additional unusable installation tokens")
		})
	}
}
