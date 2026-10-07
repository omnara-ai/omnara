package github

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func requireAPIError(t *testing.T, err error, code ErrorCode) *APIError {
	t.Helper()
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Code != code {
		t.Fatalf("error = %v, want %s", err, code)
	}
	return apiErr
}

func TestBeforeRequestBlocksBeforeSending(t *testing.T) {
	for i, name := range []string{"token", "repository resolver", "pull metadata", "comment mutation"} {
		t.Run(name, func(t *testing.T) {
			var requests atomic.Int32
			handler := withPreparedPull(func(w http.ResponseWriter, r *http.Request) {
				fmt.Fprint(w, `{"id":1}`)
			})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				handler(w, r)
			}))
			t.Cleanup(server.Close)
			var checks int
			client, err := NewClient(Config{
				Credentials: testCredentials(t), InstallationID: 456,
				APIURL: server.URL, HTTPClient: server.Client(),
				BeforeRequest: func(ctx context.Context) error {
					checks++
					deadline, ok := ctx.Deadline()
					if !ok || time.Until(deadline) > OperationTimeout {
						t.Error("hook has no bounded operation context")
					}
					if checks == i+1 {
						return context.Canceled
					}
					return nil
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.CreateDiscussionComment(t.Context(), testScope(), "hello")
			if err != context.Canceled { //nolint:errorlint // The hook contract requires the exact error, without wrapping.
				t.Fatalf("hook error = %v, want unchanged context.Canceled", err)
			}
			if checks != i+1 || requests.Load() != int32(i) {
				t.Fatalf("checks = %d, requests = %d; want %d, %d", checks, requests.Load(), i+1, i)
			}
		})
	}
}

func TestBeforeRequestRechecksOnRetry(t *testing.T) {
	var requests atomic.Int32
	client, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	})
	var checks int
	client.beforeRequest = func(ctx context.Context) error {
		checks++
		if checks == 2 {
			return context.Canceled
		}
		return nil
	}
	_, _, err := client.do(t.Context(), http.MethodGet, "/test", "token", jsonMediaType, nil, false)
	if err != context.Canceled || checks != 2 || requests.Load() != 1 { //nolint:errorlint // Require exact hook error.
		t.Fatalf("error = %v, checks = %d, requests = %d", err, checks, requests.Load())
	}
}

func TestRetryClassification(t *testing.T) {
	for _, test := range []struct {
		name   string
		status int
		body   string
		write  bool
		code   ErrorCode
		calls  int32
	}{
		{"read 503", 503, "unavailable", false, TransientFailure, 3},
		{"read 408", 408, "timeout", false, TransientFailure, 3},
		{"write 503", 503, "could have been saved", true, DeliveryUnknown, 1},
		{"write 408", 408, "timeout", true, DeliveryUnknown, 1},
		{"write 403", 403, `{"message":"Resource not accessible by integration"}`, true, PermanentFailure, 1},
		{"write 422", 422, `{"message":"Validation Failed"}`, true, PermanentFailure, 1},
		{"read 404", 404, "not found", false, PermanentFailure, 1},
		{"redirect", 302, "redirect", false, PermanentFailure, 1},
		{"write invalid JSON", 201, "invalid JSON", true, DeliveryUnknown, 1},
		{"write missing receipt", 201, `{}`, true, DeliveryUnknown, 1},
		{"read invalid JSON", 200, "invalid JSON", false, InvalidResponse, 1},
		{"read missing metadata", 200, `null`, false, InvalidResponse, 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			var calls atomic.Int32
			handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.WriteHeader(test.status)
				fmt.Fprint(w, test.body)
			})
			if test.write {
				handler = withPreparedPull(handler)
			} else {
				handler = withToken(handler)
			}
			client, _ := testClient(t, handler)
			var err error
			if test.write {
				_, err = client.CreateDiscussionComment(t.Context(), testScope(), "hello")
			} else {
				_, err = client.GetPullRequest(t.Context(), testScope())
			}
			requireAPIError(t, err, test.code)
			if calls.Load() != test.calls {
				t.Fatalf("requests = %d, want %d", calls.Load(), test.calls)
			}
		})
	}
}

func TestRateLimitsReturnWithoutSleeping(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	for _, test := range []struct {
		name   string
		status int
		header http.Header
		body   string
		want   time.Duration
	}{
		{"429", 429, http.Header{}, "", time.Minute},
		{"secondary", 403, http.Header{}, `{"message":"You have exceeded a secondary rate limit."}`, time.Minute},
		{"abuse", 403, http.Header{}, `{"message":"You have triggered an abuse detection mechanism."}`, time.Minute},
		{"explicit seconds", 403, http.Header{"Retry-After": {"120"}}, `{}`, 2 * time.Minute},
		{"explicit date", 429, http.Header{
			"Retry-After": {now.Add(3 * time.Minute).Format(http.TimeFormat)},
		}, "", 3 * time.Minute},
		{"primary reset", 403, http.Header{
			"X-Ratelimit-Remaining": {"0"}, "X-Ratelimit-Reset": {fmt.Sprint(now.Add(4 * time.Minute).Unix())},
		}, "", 4 * time.Minute},
		{"malformed retry", 429, http.Header{"Retry-After": {"-1"}}, "", time.Minute},
	} {
		t.Run(test.name, func(t *testing.T) {
			var calls atomic.Int32
			client, _ := testClient(t, withPreparedPull(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				for key, values := range test.header {
					w.Header()[key] = values
				}
				w.WriteHeader(test.status)
				fmt.Fprint(w, test.body)
			}))
			client.now = func() time.Time { return now }
			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
			defer cancel()
			_, err := client.CreateDiscussionComment(ctx, testScope(), "hello")
			apiErr := requireAPIError(t, err, RateLimited)
			if apiErr.RetryAfter != test.want || calls.Load() != 1 || ctx.Err() != nil {
				t.Fatalf("rate limit = %+v, requests = %d", apiErr, calls.Load())
			}
		})
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestContextBudgetAndAmbiguousTransportFailure(t *testing.T) {
	for _, write := range []bool{false, true} {
		t.Run(fmt.Sprint(write), func(t *testing.T) {
			var calls int
			client, _ := testClient(t, nil)
			client.tokens[0] = cachedToken{
				repositoryID: testScope().RepositoryID,
				token:        "cached", expiresAt: time.Now().Add(time.Hour),
			}
			client.tokens[1] = client.tokens[0]
			client.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
				if body := preparationJSON(r); write && body != "" {
					return fixtureResponse(r, body), nil
				}
				calls++
				deadline, ok := r.Context().Deadline()
				if !ok || time.Until(deadline) > OperationTimeout {
					t.Error("request has no bounded operation context")
				}
				return nil, errors.New("transport echoed Bearer cached and secret body")
			})
			var err error
			if write {
				_, err = client.CreateDiscussionComment(t.Context(), testScope(), "body")
				requireAPIError(t, err, DeliveryUnknown)
				if calls != 1 {
					t.Fatal("retried ambiguous POST")
				}
			} else {
				_, err = client.GetPullRequest(t.Context(), testScope())
				requireAPIError(t, err, TransientFailure)
				if calls != 3 {
					t.Fatal("unbounded read retry")
				}
			}
			if strings.Contains(err.Error(), "secret") || strings.Contains(fmt.Sprintf("%+v", err), "cached") {
				t.Fatal("transport error disclosed private content")
			}
			calls = 0
			client.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
				if body := preparationJSON(r); body != "" {
					return fixtureResponse(r, body), nil
				}
				calls++
				<-r.Context().Done()
				return nil, r.Context().Err()
			})
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Millisecond)
			defer cancel()
			_, err = client.CreateDiscussionComment(ctx, testScope(), "body")
			requireAPIError(t, err, DeliveryUnknown)
			if !errors.Is(err, context.DeadlineExceeded) || calls != 1 {
				t.Fatalf("context deadline was lost: %v", err)
			}
			calls = 0
			_, err = client.CreateDiscussionComment(ctx, testScope(), "body")
			if !errors.Is(err, context.DeadlineExceeded) || calls != 0 {
				t.Fatal("canceled call attempted I/O")
			}
		})
	}
}

type countingBody struct {
	read   int
	closed bool
}

func (b *countingBody) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 'x'
	}
	b.read += len(p)
	return len(p), nil
}

func (b *countingBody) Close() error { b.closed = true; return nil }

func TestResponseBodiesBoundedAndSanitized(t *testing.T) {
	for _, status := range []int{http.StatusForbidden, http.StatusOK} {
		client, _ := testClient(t, nil)
		body := &countingBody{}
		client.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: status, Header: http.Header{}, Body: body, Request: r}, nil
		})
		_, _, err := client.do(t.Context(), http.MethodPost, "/test", "token", jsonMediaType, nil, true)
		if err == nil || !body.closed {
			t.Fatal("expected closed bounded response")
		}
		limit := ErrorMaxBytes
		if status == http.StatusOK {
			limit = ResponseMaxBytes + 1
			requireAPIError(t, err, DeliveryUnknown)
		}
		if body.read != limit {
			t.Fatalf("read %d bytes, limit %d", body.read, limit)
		}
	}
	client, _ := testClient(t, withPreparedPull(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		fmt.Fprint(w, `{"message":"Bearer installation-token test-webhook-secret private-key private-comment"}`)
	}))
	_, err := client.CreateDiscussionComment(t.Context(), testScope(), "private-comment")
	if err == nil || strings.Contains(fmt.Sprintf("%+v", err), "private") || strings.Contains(err.Error(), "token") {
		t.Fatalf("provider error not sanitized: %v", err)
	}
}

func TestRedirectsNeverForwardCredentials(t *testing.T) {
	var hits atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1) }))
	t.Cleanup(target.Close)
	for _, tokenRedirect := range []bool{false, true} {
		for _, status := range []int{301, 302, 303, 307, 308} {
			t.Run(fmt.Sprintf("%t/%d", tokenRedirect, status), func(t *testing.T) {
				handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					http.Redirect(w, r, target.URL+"/steal", status)
				})
				if !tokenRedirect {
					handler = withToken(handler)
				}
				client, _ := testClient(t, handler)
				_, err := client.GetPullRequest(t.Context(), testScope())
				requireAPIError(t, err, PermanentFailure)
			})
		}
	}
	if hits.Load() != 0 {
		t.Fatal("followed a token-bearing redirect")
	}
}

func TestSafeRetrySuccessAndCancellation(t *testing.T) {
	var count atomic.Int32
	client, _ := testClient(t, withToken(func(w http.ResponseWriter, r *http.Request) {
		if count.Add(1) < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		fmt.Fprint(w, testPullJSON)
	}))
	if _, err := client.GetPullRequest(t.Context(), testScope()); err != nil || count.Load() != 3 {
		t.Fatalf("safe retry failed: %v", err)
	}
	count.Store(0)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Millisecond)
	defer cancel()
	_, err := client.GetPullRequest(ctx, testScope())
	if !errors.Is(err, context.DeadlineExceeded) || count.Load() != 1 {
		t.Fatalf("retry backoff ignored deadline: %v", err)
	}
	count.Store(0)
	client.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		count.Add(1)
		return &http.Response{
			StatusCode: http.StatusServiceUnavailable, Header: http.Header{"Retry-After": {"90"}},
			Body: io.NopCloser(strings.NewReader("")), Request: r,
		}, nil
	})
	_, err = client.GetPullRequest(t.Context(), testScope())
	apiErr := requireAPIError(t, err, TransientFailure)
	if count.Load() != 1 || apiErr.RetryAfter != 90*time.Second {
		t.Fatal("slept or retried before provider Retry-After")
	}
}

func TestAPIOriginValidation(t *testing.T) {
	for _, raw := range []string{
		"http://github.com", "https://user:password@api.github.com", "https://api.github.com?token=x",
		"https://api.github.com/other", "https://api.github.com#fragment", "//api.github.com", "https://api.github.com?",
	} {
		_, err := NewClient(Config{Credentials: testCredentials(t), InstallationID: 1, APIURL: raw})
		if err == nil || strings.Contains(err.Error(), "password") {
			t.Errorf("accepted or disclosed invalid origin %s", raw)
		}
	}
	transport := &http.Client{Timeout: time.Minute}
	client, err := NewClient(Config{Credentials: testCredentials(t), InstallationID: 1, HTTPClient: transport})
	if err != nil || client.http.Timeout != OperationTimeout || transport.Timeout != time.Minute {
		t.Fatal("constructor failed to bound cloned client")
	}
}

func TestCanceledPostAfterProviderAcceptsIsUnknown(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var calls atomic.Int32
	client, _ := testClient(t, withPreparedPull(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if _, err := io.Copy(io.Discard, r.Body); err != nil {
			t.Error(err)
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":`))
		_ = http.NewResponseController(w).Flush()
		cancel()
	}))
	_, err := client.CreateDiscussionComment(ctx, testScope(), "accepted comment")
	requireAPIError(t, err, DeliveryUnknown)
	if calls.Load() != 1 {
		t.Fatal("repeated a potentially accepted comment")
	}
}

func TestAuthAndAPICallShareOperationDeadline(t *testing.T) {
	client, _ := testClient(t, nil)
	var deadlines []time.Time
	client.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		deadline, ok := r.Context().Deadline()
		if !ok {
			t.Error("missing request deadline")
		}
		deadlines = append(deadlines, deadline)
		body := `{"id":1}`
		if strings.HasSuffix(r.URL.Path, "/access_tokens") {
			body = fmt.Sprintf(`{"token":"token","expires_at":%q}`, time.Now().Add(time.Hour).Format(time.RFC3339))
		} else if prepared := preparationJSON(r); prepared != "" {
			body = prepared
		}
		return &http.Response{
			StatusCode: http.StatusCreated, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body)), Request: r,
		}, nil
	})
	if _, err := client.CreateDiscussionComment(t.Context(), testScope(), "comment"); err != nil {
		t.Fatal(err)
	}
	if len(deadlines) != 4 || time.Until(deadlines[0]) > OperationTimeout {
		t.Fatal("expected authentication, resolution, PR validation and comment within one budget")
	}
	for _, deadline := range deadlines[1:] {
		if !deadlines[0].Equal(deadline) {
			t.Fatal("authentication, resolution and comment did not share the bounded context")
		}
	}
}

func fixtureResponse(r *http.Request, body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body)), Request: r,
	}
}
