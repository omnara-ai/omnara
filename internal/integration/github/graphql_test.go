package github

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGraphQLMutationRejectionVersusUncertainExecution(t *testing.T) {
	for _, tt := range []struct {
		name, body string
		code       ErrorCode
	}{
		{"pre-execution", `{"errors":[{"message":"private-detail"}]}`, PermanentFailure},
		{"validation", `{"data":{"addPullRequestReviewThread":null},` +
			`"errors":[{"type":"UNPROCESSABLE","message":"private-detail"}]}`, PermanentFailure},
		{"denied", `{"data":null,"errors":[{"type":"FORBIDDEN","message":"private-detail"}]}`, PermanentFailure},
		{"limited-via-header", `{"errors":[{"message":"private-detail"}]}`, RateLimited},
		{"limited-before-execution", `{"errors":[{"type":"RATE_LIMITED"}]}`, RateLimited},
		{"limited", `{"data":null,"errors":[{"type":"RATE_LIMITED"}]}`, RateLimited},
		{"partial", `{"data":{"addPullRequestReviewThread":{"thread":null}},` +
			`"errors":[{"type":"UNPROCESSABLE"}]}`, DeliveryUnknown},
		{"execution", `{"data":null,"errors":[{"message":"private-detail"}]}`, DeliveryUnknown},
		{"execution-at-quota", `{"data":null,"errors":[{"message":"private-detail"}]}`, DeliveryUnknown},
	} {
		t.Run(tt.name, func(t *testing.T) {
			requests := 0
			client, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				requests++
				assert.Equal(t, "/graphql", r.URL.Path)
				if tt.name == "execution-at-quota" || tt.name == "limited-via-header" {
					w.Header().Set("X-Ratelimit-Remaining", "0")
				}
				fmt.Fprint(w, tt.body)
			})
			var result any
			err := client.graphQL(t.Context(), "token", "mutation test", nil, &result, true)
			requireAPIError(t, err, tt.code)
			require.NotContains(t, err.Error(), "private-detail")
			require.Equal(t, 1, requests)
		})
	}
}
