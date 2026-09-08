package modelprovider

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
	"testing/synctest"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/model/anthropicmessages"
	"github.com/omnara-ai/omnara/internal/model/route"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
	"github.com/omnara-ai/omnara/internal/storage/modelstore"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMessagesOutputAllowanceMetadata(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		body   string
		status int
		want   int
	}{
		{name: "published", body: `{"max_tokens":128000}`, want: 128000},
		{name: "small published limit", body: `{"max_tokens":8192}`, want: 8192},
		{name: "null", body: `{"max_tokens":null}`},
		{name: "missing", body: `{}`},
		{name: "other metadata is not an output limit", body: `{"max_input_tokens":200000,"max_output_tokens":32000}`},
		{name: "zero", body: `{"max_tokens":0}`},
		{name: "negative", body: `{"max_tokens":-1}`},
		{name: "fraction", body: `{"max_tokens":1.5}`},
		{name: "string", body: `{"max_tokens":"64000"}`},
		{name: "invalid JSON", body: `{"max_tokens":`},
		{name: "unauthorized", status: http.StatusUnauthorized, body: `{"max_tokens":128000}`},
		{name: "unavailable", status: http.StatusServiceUnavailable},
		{name: "oversized", body: strings.Repeat(" ", discoveryMaxResponseSize) + `{"max_tokens":128000}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if tc.status != 0 {
					w.WriteHeader(tc.status)
				}
				_, _ = io.WriteString(w, tc.body)
			}))
			t.Cleanup(server.Close)
			resolver := Resolver{AllowLoopback: true, MessagesOutputLimits: &MessagesOutputLimits{}}
			limit, err := resolver.messagesOutputAllowance(context.Background(), modelstore.ModelProviderConfigRecord{
				BaseURL: server.URL,
			}, uuid.New(), "test-model", route.Headers{})
			require.NoError(t, err)
			want := tc.want
			if want == 0 {
				want = messagesFallbackOutputTokens
			}
			require.Equal(t, want, limit)
		})
	}
}

func TestFetchMessagesOutputLimitPreservesRouteAndAlias(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Equal(t, "/proxy/v1/models/team%2Falias%3F%23%20latest", r.URL.EscapedPath())
		assert.Empty(t, r.URL.RawQuery)
		assert.Equal(t, "test-key", r.Header.Get("X-Provider-Key"))
		assert.Equal(t, anthropicmessages.APIVersion, r.Header.Get("Anthropic-Version"))
		_, _ = io.WriteString(w, `{"id":"resolved-model-version","max_tokens":96000}`)
	}))
	t.Cleanup(server.Close)
	provider := discoveryProviderConfig(server.URL+"/proxy/v1", modelprotocol.APIFormatAnthropicMessages,
		modelstore.ModelProviderAuthKindAPIKeyHeader, `{"header_name":"X-Provider-Key"}`)
	auth, err := routeAuthForProviderConfig(provider, "test-key")
	require.NoError(t, err)
	limit, err := fetchMessagesOutputLimit(
		context.Background(), provider.BaseURL, "team/alias?# latest", auth, server.Client(),
	)
	require.NoError(t, err)
	require.Equal(t, 96000, limit)
}

func TestFetchMessagesOutputLimitRejectsRedirects(t *testing.T) {
	t.Parallel()
	var followed atomic.Bool
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		followed.Store(true)
		_, _ = io.WriteString(w, `{"max_tokens":128000}`)
	}))
	t.Cleanup(target.Close)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	t.Cleanup(server.Close)
	_, err := fetchMessagesOutputLimit(context.Background(), server.URL, "test-model", route.Headers{}, server.Client())
	require.Error(t, err)
	require.False(t, followed.Load())
}

type messagesLimitRoundTripper func(*http.Request) (*http.Response, error)

func (f messagesLimitRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func TestFetchMessagesOutputLimitBoundsDeadline(t *testing.T) {
	t.Parallel()
	for _, parentTimeout := range []time.Duration{0, time.Second} {
		t.Run(parentTimeout.String(), func(t *testing.T) {
			t.Parallel()
			synctest.Test(t, func(t *testing.T) {
				ctx := t.Context()
				want := messagesLimitLookupTimeout
				if parentTimeout > 0 {
					var cancel context.CancelFunc
					ctx, cancel = context.WithTimeout(ctx, parentTimeout)
					defer cancel()
					want = parentTimeout
				}
				client := &http.Client{Transport: messagesLimitRoundTripper(func(r *http.Request) (*http.Response, error) {
					<-r.Context().Done()
					return nil, r.Context().Err()
				})}
				before := time.Now()
				_, err := fetchMessagesOutputLimit(ctx, "https://provider.example/v1", "test-model", route.Headers{}, client)
				require.ErrorIs(t, err, context.DeadlineExceeded)
				require.Equal(t, want, time.Since(before))
				if parentTimeout == 0 {
					require.NoError(t, ctx.Err(), "the lookup deadline must not cancel its parent")
				}
			})
		})
	}
}

func TestMessagesOutputAllowanceTransportAndBedrock(t *testing.T) {
	t.Parallel()
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		_, _ = io.WriteString(w, `{"max_tokens":96000}`)
	}))
	t.Cleanup(server.Close)
	provider := modelstore.ModelProviderConfigRecord{BaseURL: server.URL}
	resolver := Resolver{}
	limit, err := resolver.messagesOutputAllowance(context.Background(), provider, uuid.Nil, "test-model", route.Headers{})
	require.NoError(t, err)
	require.Equal(t, messagesFallbackOutputTokens, limit)
	require.Zero(t, requests.Load(), "runtime metadata lookup must retain SSRF protection")

	resolver.AllowLoopback = true
	for range 2 {
		limit, err = resolver.messagesOutputAllowance(context.Background(), provider, uuid.Nil, "test-model", route.Headers{})
		require.NoError(t, err)
		require.Equal(t, 96000, limit)
	}
	require.EqualValues(t, 2, requests.Load(), "a nil cache performs uncached lookups")

	provider.APIVariant = modelprotocol.APIVariantBedrock
	limit, err = resolver.messagesOutputAllowance(context.Background(), provider, uuid.Nil, "test-model", route.Headers{})
	require.NoError(t, err)
	require.Equal(t, messagesFallbackOutputTokens, limit)
	require.EqualValues(t, 2, requests.Load(), "Bedrock must skip the direct Models API")
}

func TestMessagesOutputLimitsCachesSuccessAndFailure(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		tokens int
		err    error
		ttl    time.Duration
	}{
		{name: "success", tokens: 96000, ttl: messagesLimitCacheTTL},
		{name: "missing metadata", ttl: messagesLimitFailureTTL},
		{name: "lookup timeout", err: context.DeadlineExceeded, ttl: messagesLimitFailureTTL},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			synctest.Test(t, func(t *testing.T) {
				cache := &MessagesOutputLimits{}
				key := messagesOutputLimitKey{slug: "test-model"}
				calls := 0
				fetch := func(context.Context) (int, error) {
					calls++
					if calls > 1 {
						return 128000, nil
					}
					return tc.tokens, tc.err
				}
				for index := range 2 {
					if index > 0 {
						<-time.After(tc.ttl - time.Nanosecond)
					}
					limit, err := cache.lookup(t.Context(), key, fetch)
					require.NoError(t, err)
					require.Equal(t, tc.tokens, limit)
					require.Equal(t, 1, calls)
				}
				<-time.After(2 * time.Nanosecond)
				limit, err := cache.lookup(t.Context(), key, fetch)
				require.NoError(t, err)
				require.Equal(t, 128000, limit)
				require.Equal(t, 2, calls)
			})
		})
	}
}

func TestMessagesOutputAllowanceCacheIdentity(t *testing.T) {
	t.Parallel()
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		_, _ = io.WriteString(w, `{"max_tokens":96000}`)
	}))
	t.Cleanup(server.Close)
	base := modelstore.ModelProviderConfigRecord{ID: uuid.New(), UpdatedAt: time.Now(), BaseURL: server.URL}
	version := uuid.New()
	resolver := Resolver{AllowLoopback: true, MessagesOutputLimits: &MessagesOutputLimits{}}
	for index, dimension := range []string{"initial", "provider", "updated provider", "credential version", "slug"} {
		provider, credentialVersion, slug := base, version, "test-model"
		switch dimension {
		case "provider":
			provider.ID = uuid.New()
		case "updated provider":
			provider.UpdatedAt = base.UpdatedAt.Add(time.Second)
		case "credential version":
			credentialVersion = uuid.New()
		case "slug":
			slug = "another-model"
		}
		for range 2 {
			limit, err := resolver.messagesOutputAllowance(
				context.Background(), provider, credentialVersion, slug, route.Headers{},
			)
			require.NoError(t, err)
			require.Equal(t, 96000, limit)
		}
		require.EqualValues(t, index+1, requests.Load(), dimension)
	}
	requestsBefore := requests.Load()
	base.UpdatedAt = base.UpdatedAt.UTC().Round(0)
	_, err := resolver.messagesOutputAllowance(context.Background(), base, version, "test-model", route.Headers{})
	require.NoError(t, err)
	require.Equal(t, requestsBefore, requests.Load(), "equivalent timestamp representations must reuse the entry")
}

func TestMessagesOutputLimitsBoundsCache(t *testing.T) {
	t.Parallel()
	cache := &MessagesOutputLimits{}
	calls := 0
	fetch := func(context.Context) (int, error) { calls++; return 96000, nil }
	for index := range messagesLimitCacheSize + 1 {
		_, err := cache.lookup(context.Background(), messagesOutputLimitKey{slug: fmt.Sprint(index)}, fetch)
		require.NoError(t, err)
	}
	require.Equal(t, messagesLimitCacheSize, cache.entries.Len())
	_, err := cache.lookup(context.Background(), messagesOutputLimitKey{slug: "0"}, fetch)
	require.NoError(t, err)
	require.Equal(t, messagesLimitCacheSize+2, calls, "the oldest entry must be evicted")
}

func TestMessagesOutputLimitsCoalescesWithoutBlockingOtherKeys(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		cache := &MessagesOutputLimits{}
		key := messagesOutputLimitKey{slug: "test-model"}
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		release := make(chan struct{})
		go func() {
			limit, err := cache.lookup(ctx, key, func(ctx context.Context) (int, error) {
				select {
				case <-release:
					return 96000, nil
				case <-ctx.Done():
					return 0, ctx.Err()
				}
			})
			assert.NoError(t, err)
			assert.Equal(t, 96000, limit)
		}()
		synctest.Wait()
		var duplicateFetches atomic.Int64
		startWaiter := func(ctx context.Context) <-chan error {
			done := make(chan error, 1)
			go func() {
				limit, err := cache.lookup(ctx, key, func(context.Context) (int, error) {
					duplicateFetches.Add(1)
					return 96000, nil
				})
				if err == nil {
					assert.Equal(t, 96000, limit)
				}
				done <- err
			}()
			return done
		}
		waiterCtx, cancelWaiter := context.WithCancel(ctx)
		defer cancelWaiter()
		canceledWaiter := startWaiter(waiterCtx)
		liveWaiter := startWaiter(ctx)
		synctest.Wait()
		cancelWaiter()
		require.ErrorIs(t, <-canceledWaiter, context.Canceled)
		_, err := cache.lookup(ctx, messagesOutputLimitKey{slug: "other-model"}, func(context.Context) (int, error) {
			return 128000, nil
		})
		require.NoError(t, err, "an unrelated lookup must finish while the first remains in flight")
		close(release)
		require.NoError(t, <-liveWaiter)
		synctest.Wait()
		require.Zero(t, duplicateFetches.Load())
	})
}

func TestMessagesOutputLimitsCanceledLeaderDoesNotCache(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		cache := &MessagesOutputLimits{}
		key := messagesOutputLimitKey{slug: "test-model"}
		leaderCtx, cancelLeader := context.WithCancel(t.Context())
		defer cancelLeader()
		leaderDone := make(chan error, 1)
		go func() {
			_, err := cache.lookup(leaderCtx, key, func(ctx context.Context) (int, error) {
				<-ctx.Done()
				return 0, ctx.Err()
			})
			leaderDone <- err
		}()
		synctest.Wait()
		var retries atomic.Int64
		go func() {
			limit, err := cache.lookup(t.Context(), key, func(context.Context) (int, error) {
				retries.Add(1)
				return 128000, nil
			})
			assert.NoError(t, err)
			assert.Equal(t, 128000, limit)
		}()
		synctest.Wait()
		cancelLeader()
		require.ErrorIs(t, <-leaderDone, context.Canceled)
		synctest.Wait()
		require.EqualValues(t, 1, retries.Load(), "a live waiter must retry after the canceled leader")
		limit, err := cache.lookup(t.Context(), key, func(context.Context) (int, error) {
			return 0, errors.New("unexpected extra lookup")
		})
		require.NoError(t, err)
		require.Equal(t, 128000, limit)
	})
}

func TestMessagesOutputLimitsReleasesPanickedLookup(t *testing.T) {
	t.Parallel()
	cache := &MessagesOutputLimits{}
	key := messagesOutputLimitKey{slug: "test-model"}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	require.Panics(t, func() {
		_, _ = cache.lookup(ctx, key, func(context.Context) (int, error) {
			panic("failed lookup")
		})
	})
	limit, err := cache.lookup(ctx, key, func(context.Context) (int, error) {
		return 96000, nil
	})
	require.NoError(t, err)
	require.Equal(t, 96000, limit)
}
