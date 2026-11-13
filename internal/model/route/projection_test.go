package route

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/omnara-ai/omnara/internal/model"
	"github.com/stretchr/testify/require"
)

type countingProjectionAuth struct{ calls int }

func (a *countingProjectionAuth) Apply(*http.Request) error {
	a.calls++
	return nil
}

func TestProjectMatchesPrepareWithoutApplyingCredentials(t *testing.T) {
	auth := &countingProjectionAuth{}
	client := Client{
		Endpoint: StaticEndpoint{BaseURL: "https://example.test", Path: "/respond"},
		Auth:     auth,
		Protocol: &fakeProtocol{buildBody: json.RawMessage(`{"model":"fake","input":[]}`)},
	}
	projected, err := client.Project(t.Context(), model.PrepareInput{})
	require.NoError(t, err)
	require.Zero(t, auth.calls)
	prepared, err := client.Prepare(t.Context(), model.PrepareInput{})
	require.NoError(t, err)
	require.Equal(t, 1, auth.calls)
	require.Equal(t, prepared, projected, "pure projection must describe the exact prepared input")
}
