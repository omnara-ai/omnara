package channelconnector

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOperationFailureDoesNotForwardProviderDiagnostics(t *testing.T) {
	for _, raw := range []string{
		`{"code":"invalid_address"}`,
		`{"code":"address_unavailable"}`,
		`{"code":"unsupported_address"}`,
	} {
		_, err := DecodeOperationFailure(json.RawMessage(raw))
		require.NoError(t, err)
	}
	for _, raw := range []string{
		`{"Code":"invalid_address"}`,
		`{"code":"invalid_address","code":"address_unavailable"}`,
		`{"code":"invalid_address","detail":"provider token secret"}`,
		`{"code":"provider token secret"}`,
		`{"code":"invalid_address","metadata":{"token":"secret"}}`,
		`{"code":"invalid_address","metadata":{}}`,
		`{"code":"invalid_address","metadata":null}`,
		`{"code":"review_not_owned"}`,
	} {
		_, err := DecodeOperationFailure(json.RawMessage(raw))
		require.Error(t, err, raw)
		require.EqualError(t, err, "invalid channel operation failure")
	}
}

func TestOperationsClientPreservesOnlyFixedFailureFacts(t *testing.T) {
	for _, outcome := range []OperationOutcome{OperationFailed, OperationUnknown} {
		for _, valid := range []bool{false, true} {
			t.Run(string(outcome)+map[bool]string{false: "/untrusted", true: "/fixed"}[valid], func(t *testing.T) {
				payload := `{"code":"invalid_address"}`
				if !valid {
					payload = `{"code":"invalid_address","detail":"raw-provider-secret"}`
				}
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					_, _ = io.Copy(io.Discard, r.Body)
					w.Header().Set("Content-Type", "application/json")
					_ = json.NewEncoder(w).Encode(OperationResult{
						RequestID: "request-1", Outcome: outcome, Payload: json.RawMessage(payload),
					})
				}))
				defer server.Close()
				result, err := operationClient(t, operationConfig(t, server.URL)).Execute(operationContext(t), operationRequest())
				assertOperationError(t, result, err, outcome, "gateway_"+string(outcome))
				if valid && outcome == OperationFailed {
					require.JSONEq(t, payload, string(result.Payload))
				} else {
					require.Empty(t, result.Payload)
				}
			})
		}
	}
}
