package github

import (
	"encoding/json"
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAcknowledgeComment(t *testing.T) {
	for _, endpoint := range []string{"issues", "pulls"} {
		for _, status := range []int{http.StatusOK, http.StatusCreated, http.StatusForbidden} {
			t.Run(endpoint+"/"+http.StatusText(status), func(t *testing.T) {
				var reactions atomic.Int32
				client, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path == "/app/installations/456/access_tokens" {
						var grant struct {
							RepositoryIDs []int64           `json:"repository_ids"`
							Permissions   map[string]string `json:"permissions"`
						}
						assert.NoError(t, json.NewDecoder(r.Body).Decode(&grant))
						assert.Equal(t, []int64{789}, grant.RepositoryIDs)
						assert.Equal(t, map[string]string{"pull_requests": "write", "metadata": "read"}, grant.Permissions)
						tokenResponse(w)
						return
					}
					if servePreparedPull(w, r) {
						return
					}
					reactions.Add(1)
					assert.Equal(t, http.MethodPost, r.Method)
					assert.Equal(t, repoPath(testRepository())+"/"+endpoint+"/comments/3001/reactions", r.URL.Path)
					assert.Equal(t, "Bearer installation-token", r.Header.Get("Authorization"))
					var body map[string]string
					assert.NoError(t, json.NewDecoder(r.Body).Decode(&body))
					assert.Equal(t, map[string]string{"content": "eyes"}, body)
					w.WriteHeader(status)
					assert.NoError(t, json.NewEncoder(w).Encode(map[string]any{"id": 1001, "content": "eyes"}))
				})
				err := client.AcknowledgeComment(t.Context(), testScope(), 3001, endpoint == "pulls")
				if status == http.StatusForbidden {
					var apiError *APIError
					require.ErrorAs(t, err, &apiError)
					assert.Equal(t, PermanentFailure, apiError.Code)
				} else {
					require.NoError(t, err)
				}
				require.Equal(t, int32(1), reactions.Load())
			})
		}
	}
}
