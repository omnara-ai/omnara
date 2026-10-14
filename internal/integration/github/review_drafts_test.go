package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func reviewFixture(r *http.Request, state string) reviewRecord {
	review := reviewRecord{
		Review: Review{ID: 80, State: state, CommitID: "abc123", User: User{ID: 999, Login: "helper[bot]", Type: "Bot"},
			HTMLURL: "https://github.com/octo-org/repo/pull/42#pullrequestreview-80"},
		NodeID: "PRR_80", PullRequestURL: "http://" + r.Host + pullPath(testRepository(), 42),
	}
	if state != "PENDING" {
		now := time.Now().UTC()
		review.SubmittedAt = &now
	}
	return review
}

func TestPendingReviewDiscoveryAndReadBack(t *testing.T) {
	for _, scenario := range []string{"none", "pending", "page-two", "foreign-author", "foreign-pr", "renamed-bot"} {
		t.Run(scenario, func(t *testing.T) {
			reads := 0
			client, _ := testClient(t, withPreparedPull(func(w http.ResponseWriter, r *http.Request) {
				reads++
				assert.Equal(t, http.MethodGet, r.Method)
				assert.Equal(t, pullPath(testRepository(), 42)+"/reviews", r.URL.Path)
				assert.Equal(t, "30", r.URL.Query().Get("per_page"))
				reviews := []reviewRecord{}
				review := reviewFixture(r, "PENDING")
				if scenario == "page-two" && reads == 1 {
					assert.Equal(t, "1", r.URL.Query().Get("page"))
					w.Header().Set("Link", "<http://"+r.Host+r.URL.Path+"?page=2&per_page=30>; rel=\"next\"")
					reviews = append(reviews, reviewFixture(r, "COMMENTED"))
				} else {
					if scenario == "page-two" {
						assert.Equal(t, "2", r.URL.Query().Get("page"))
					}
					if scenario == "foreign-author" {
						review.User.ID++
					}
					if scenario == "renamed-bot" {
						review.User.Login = "renamed[bot]"
					}
					if scenario == "foreign-pr" {
						review.PullRequestURL = "http://" + r.Host + pullPath(testRepository(), 43)
					}
					if scenario != "none" {
						reviews = append(reviews, review)
					}
				}
				assert.NoError(t, json.NewEncoder(w).Encode(reviews))
			}))
			review, found, err := client.GetPendingReview(t.Context(), testScope(), 999)
			if scenario == "foreign-author" || scenario == "foreign-pr" {
				requireAPIError(t, err, ScopeMismatch)
				return
			}
			require.NoError(t, err)
			require.Equal(t, scenario != "none", found)
			if found {
				require.EqualValues(t, 80, review.ID)
			}
			if scenario == "page-two" {
				require.Equal(t, 2, reads)
			} else {
				require.Equal(t, 1, reads)
			}
		})
	}
}

func TestReviewMutationsRejectForeignOrSubmittedDrafts(t *testing.T) {
	for _, operation := range []string{"append", "submit", "discard", "read"} {
		for _, scenario := range []string{"foreign-pr", "wrong-id", "submitted", "revoked"} {
			t.Run(operation+"/"+scenario, func(t *testing.T) {
				revoked := false
				client, _ := testClient(t, withPreparedPull(func(w http.ResponseWriter, r *http.Request) {
					assert.Equal(t, http.MethodGet, r.Method, "invalid review must never reach a mutation")
					review := reviewFixture(r, "PENDING")
					switch scenario {
					case "foreign-pr":
						review.PullRequestURL = "http://" + r.Host + pullPath(testRepository(), 43)
					case "wrong-id":
						review.ID++
					case "submitted":
						review.State = "COMMENTED"
					case "revoked":
						revoked = true
					}
					assert.NoError(t, json.NewEncoder(w).Encode(review))
				}))
				client.beforeRequest = func(_ context.Context) error {
					if revoked {
						return errors.New("authority revoked")
					}
					return nil
				}
				var err error
				switch operation {
				case "append":
					_, err = client.AddPendingReviewComment(t.Context(), testScope(), 80,
						ReviewCommentArgs{Body: "finding", Path: "file.go", Line: 7, Side: "RIGHT"})
				case "discard":
					_, err = client.DiscardReview(t.Context(), testScope(), 80)
				case "submit":
					_, err = client.SubmitReview(t.Context(), testScope(), 80, "Summary")
				case "read":
					_, err = client.GetReview(t.Context(), testScope(), 80)
				}
				if operation == "read" && (scenario == "submitted" || scenario == "revoked") {
					require.NoError(t, err)
				} else if scenario == "foreign-pr" || scenario == "wrong-id" {
					requireAPIError(t, err, ScopeMismatch)
				} else {
					require.Error(t, err)
				}
			})
		}
	}
}

func TestReviewMutationUncertainResultsAreNotRetried(t *testing.T) {
	for _, operation := range []string{"start", "append", "submit", "discard"} {
		for _, scenario := range []string{"server-error", "malformed", "missing-result", "partial-data"} {
			t.Run(operation+"/"+scenario, func(t *testing.T) {
				mutations := 0
				client, _ := testClient(t, withPreparedPull(func(w http.ResponseWriter, r *http.Request) {
					if r.Method == http.MethodGet {
						_ = json.NewEncoder(w).Encode(reviewFixture(r, "PENDING"))
						return
					}
					mutations++
					switch scenario {
					case "server-error":
						w.WriteHeader(http.StatusInternalServerError)
					case "malformed":
						fmt.Fprint(w, "{")
					case "missing-result":
						fmt.Fprint(w, `{}`)
					case "partial-data":
						fmt.Fprint(w, `{"data":{"addPullRequestReviewThread":{"thread":null}},"errors":[{"message":"private-detail"}]}`)
					}
				}))
				var err error
				switch operation {
				case "start":
					_, err = client.StartReview(t.Context(), testScope(), "abc123")
				case "append":
					_, err = client.AddPendingReviewComment(t.Context(), testScope(), 80,
						ReviewCommentArgs{Body: "finding", Path: "file.go", Line: 7, Side: "RIGHT"})
				case "discard":
					_, err = client.DiscardReview(t.Context(), testScope(), 80)
				case "submit":
					_, err = client.SubmitReview(t.Context(), testScope(), 80, "Summary")
				}
				requireAPIError(t, err, DeliveryUnknown)
				require.NotContains(t, err.Error(), "private-detail")
				require.Equal(t, 1, mutations)
			})
		}
	}
}

func TestReviewDraftInputValidation(t *testing.T) {
	client, _ := testClient(t, func(http.ResponseWriter, *http.Request) { t.Fatal("invalid input reached GitHub") })
	_, err := client.DiscardReview(t.Context(), testScope(), 0)
	require.Error(t, err)
	_, err = client.StartReview(t.Context(), testScope(), " ")
	require.Error(t, err)
	_, err = client.SubmitReview(t.Context(), testScope(), 80, " ")
	require.Error(t, err)
	_, err = client.SubmitReview(t.Context(), testScope(), 0, "Summary")
	require.Error(t, err)
	_, err = client.GetReview(t.Context(), testScope(), -1)
	require.Error(t, err)
	_, _, err = client.GetPendingReview(t.Context(), testScope(), 0)
	require.Error(t, err)
	_, err = client.ListReviewCommentsForReview(t.Context(), testScope(), 0, PageOptions{})
	require.Error(t, err)
	for _, args := range []ReviewCommentArgs{
		{Body: "finding", CommitID: "abc123", Path: "file.go", Line: 7, Side: "RIGHT"},
		{Body: "finding", Path: "file.go", Line: 0, Side: "RIGHT"},
		{Body: strings.Repeat("x", CommentMaxBytes+1), Path: "file.go", Line: 7, Side: "RIGHT"},
	} {
		_, err := client.AddPendingReviewComment(t.Context(), testScope(), 80, args)
		require.Error(t, err)
	}
}

func TestPendingReviewRejectionGuidance(t *testing.T) {
	for _, shape := range []string{"validation", "object", "string"} {
		conflict := shape != "validation"
		for _, operation := range []string{"start", "comment", "reply"} {
			t.Run(fmt.Sprintf("%s/%s", operation, shape), func(t *testing.T) {
				client, _ := testClient(t, withPreparedPull(func(w http.ResponseWriter, r *http.Request) {
					if r.Method == http.MethodGet {
						assert.NoError(t, json.NewEncoder(w).Encode(ReviewComment{DiscussionComment: DiscussionComment{ID: 31},
							PullRequestURL: "http://" + r.Host + pullPath(testRepository(), 42)}))
						return
					}
					w.WriteHeader(http.StatusUnprocessableEntity)
					if shape == "string" {
						fmt.Fprint(w, `{"errors":["User can only have one pending review per pull request"]}`)
					} else if conflict {
						fmt.Fprint(w, `{"message":"private-detail","errors":[{"resource":"PullRequestReview",`+
							`"code":"custom","message":"User can only have one pending review per pull request"}]}`)
					} else {
						fmt.Fprint(w, `{"message":"private-detail"}`)
					}
				}))
				var err error
				if operation == "start" {
					_, err = client.StartReview(t.Context(), testScope(), "abc123")
				} else if operation == "reply" {
					_, err = client.Reply(t.Context(), testScope(), 31, "reply")
				} else {
					_, err = client.CreateReviewComment(t.Context(), testScope(), ReviewCommentArgs{
						CommitID: "abc123", Body: "finding", Path: "file.go", Line: 7, Side: "RIGHT",
					})
				}
				requireAPIError(t, err, PermanentFailure)
				require.ErrorContains(t, err, "pending_review")
				require.NotContains(t, err.Error(), "private-detail")
				if conflict {
					require.ErrorContains(t, err, "already has a pending review")
					require.ErrorContains(t, err, "discard_review")
				} else {
					if operation != "reply" {
						require.ErrorContains(t, err, "commit_id")
					}
					require.NotContains(t, err.Error(), "already has a pending review")
				}
			})
		}
	}
}

func TestDraftCommentChecksSavedReviewAndState(t *testing.T) {
	for _, scenario := range []string{"pending", "published", "wrong-review", "null-thread"} {
		t.Run(scenario, func(t *testing.T) {
			client, _ := testClient(t, withPreparedPull(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodGet {
					_ = json.NewEncoder(w).Encode(reviewFixture(r, "PENDING"))
					return
				}
				if scenario == "null-thread" {
					fmt.Fprint(w, `{"data":{"addPullRequestReviewThread":{"thread":null}}}`)
					return
				}
				state, id := "PENDING", "80"
				if scenario == "published" {
					state = "SUBMITTED"
				}
				if scenario == "wrong-review" {
					id = "81"
				}
				fmt.Fprintf(w, `{"data":{"addPullRequestReviewThread":{"thread":{"comments":{"nodes":[`+
					`{"fullDatabaseId":"123","state":%q,"pullRequestReview":{"fullDatabaseId":%q}}]}}}}}`, state, id)
			}))
			result, err := client.AddPendingReviewComment(t.Context(), testScope(), 80, ReviewCommentArgs{
				Body: "finding", Path: "file.go", Line: 7, Side: "RIGHT",
			})
			if scenario == "pending" {
				require.NoError(t, err)
				require.EqualValues(t, 123, result.ID)
				require.EqualValues(t, 80, result.ReviewID)
			} else {
				requireAPIError(t, err, DeliveryUnknown)
				if scenario == "null-thread" {
					require.ErrorContains(t, err, "review commit diff")
				}
			}
		})
	}
}

func TestReviewCommentPageRemainsOnAssignedReview(t *testing.T) {
	for _, scenario := range []string{"matching", "foreign-review", "foreign-pr"} {
		t.Run(scenario, func(t *testing.T) {
			client, _ := testClient(t, withPreparedPull(func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, pullPath(testRepository(), 42)+"/reviews/80/comments", r.URL.Path)
				comment := ReviewComment{PullRequestReviewID: 80,
					PullRequestURL: "http://" + r.Host + pullPath(testRepository(), 42)}
				if scenario == "foreign-review" {
					comment.PullRequestReviewID++
				}
				if scenario == "foreign-pr" {
					comment.PullRequestURL = "http://" + r.Host + pullPath(testRepository(), 43)
				}
				assert.NoError(t, json.NewEncoder(w).Encode([]ReviewComment{comment}))
			}))
			_, err := client.ListReviewCommentsForReview(t.Context(), testScope(), 80, PageOptions{})
			if scenario == "matching" {
				require.NoError(t, err)
			} else {
				requireAPIError(t, err, ScopeMismatch)
			}
		})
	}
}

func TestReviewCommitNormalizationAndNullableHistory(t *testing.T) {
	for _, operation := range []string{"start", "read", "submit"} {
		t.Run(operation, func(t *testing.T) {
			client, _ := testClient(t, withPreparedPull(func(w http.ResponseWriter, r *http.Request) {
				review := reviewFixture(r, "PENDING")
				if operation == "read" || (operation == "submit" && r.Method == http.MethodPost) {
					assert.NoError(t, json.NewEncoder(w).Encode(struct {
						reviewRecord
						CommitID *string `json:"commit_id"`
					}{reviewRecord: reviewFixture(r, "COMMENTED")}))
					return
				}
				assert.NoError(t, json.NewEncoder(w).Encode(review))
			}))
			if operation == "start" {
				review, err := client.StartReview(t.Context(), testScope(), "ABC123")
				require.NoError(t, err)
				require.Equal(t, "abc123", review.CommitID)
			} else if operation == "read" {
				review, err := client.GetReview(t.Context(), testScope(), 80)
				require.NoError(t, err)
				require.Empty(t, review.CommitID)
			} else {
				review, err := client.SubmitReview(t.Context(), testScope(), 80, "Summary")
				require.NoError(t, err)
				require.Equal(t, "COMMENTED", review.State)
				require.Empty(t, review.CommitID)
			}
		})
	}
}
