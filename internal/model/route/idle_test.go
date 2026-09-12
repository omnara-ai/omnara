package route

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/storage/modelstore"
	"github.com/stretchr/testify/require"
)

type idleTestBody struct {
	read  func([]byte) (int, error)
	close func() error
}

func (b idleTestBody) Read(p []byte) (int, error) { return b.read(p) }
func (b idleTestBody) Close() error {
	if b.close != nil {
		return b.close()
	}
	return nil
}

func waitForResponseBytes(ctx context.Context, delay time.Duration) error {
	select {
	case <-time.After(delay):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func TestStreamingTimeouts(t *testing.T) {
	for _, tc := range []struct {
		name                                              string
		headers, interval, processing, total, cancelAfter time.Duration
		chunks                                            int
		want                                              error
		elapsed                                           time.Duration
	}{
		{name: "header stall", headers: 6 * time.Minute, want: errProviderIdleTimeout, elapsed: 5 * time.Minute},
		{
			name:     "body stall",
			interval: 6 * time.Minute,
			chunks:   1,
			want:     errProviderIdleTimeout,
			elapsed:  5 * time.Minute,
		},
		{name: "heartbeats beyond ten minutes", interval: time.Minute, chunks: 20, elapsed: 20 * time.Minute},
		{
			name:       "slow consumer",
			interval:   time.Minute,
			processing: 6 * time.Minute,
			chunks:     3,
			elapsed:    21 * time.Minute,
		},
		{
			name:     "progressing response reaches total",
			interval: time.Minute,
			chunks:   70,
			total:    time.Hour,
			want:     context.DeadlineExceeded,
			elapsed:  time.Hour,
		},
		{
			name:        "parent cancellation before headers",
			headers:     6 * time.Minute,
			cancelAfter: 2 * time.Minute,
			want:        context.Canceled,
			elapsed:     2 * time.Minute,
		},
		{
			name:        "parent cancellation during body",
			interval:    6 * time.Minute,
			chunks:      1,
			cancelAfter: 2 * time.Minute,
			want:        context.Canceled,
			elapsed:     2 * time.Minute,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				if tc.cancelAfter > 0 {
					timer := time.AfterFunc(tc.cancelAfter, cancel)
					defer timer.Stop()
				}
				requestContexts := make(chan context.Context, 1)
				closed := false
				reads := 0
				roundTrip := func(req *http.Request) (*http.Response, error) {
					requestContexts <- req.Context()
					if err := waitForResponseBytes(req.Context(), tc.headers); err != nil {
						return nil, err
					}
					body := idleTestBody{
						read: func(p []byte) (int, error) {
							if reads >= tc.chunks {
								return 0, io.EOF
							}
							if err := waitForResponseBytes(req.Context(), tc.interval); err != nil {
								return 0, err
							}
							reads++
							return copy(p, ": heartbeat\n\ndata: progress\n\n"), nil
						},
						close: func() error { closed = true; return nil },
					}
					return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: body}, nil
				}
				transport := HTTPTransport{Client: &http.Client{Timeout: tc.total, Transport: roundTripFunc(roundTrip)}}

				start := time.Now()
				resp, err := transport.StreamingDo(
					ctx,
					"https://example.test/respond",
					[]byte(`{}`),
					nil,
					ServerSentEventsMediaType,
				)
				if err == nil {
					err = ReadSSEEvents(ctx, resp.Body, func(SSEEvent) error {
						time.Sleep(tc.processing) //nolint:omnaralint // Advance synctest's virtual clock.
						return nil
					})
					require.NoError(t, resp.Body.Close())
					if !closed {
						t.Fatal("body was not closed")
					}
				}
				if !errors.Is(err, tc.want) {
					t.Fatalf("error=%v want=%v", err, tc.want)
				}
				if !errors.Is(tc.want, errProviderIdleTimeout) && errors.Is(err, errProviderIdleTimeout) {
					t.Fatalf("wrong timeout cause: %v", err)
				}
				if elapsed := time.Since(start); elapsed != tc.elapsed {
					t.Fatalf("elapsed=%s want=%s", elapsed, tc.elapsed)
				}
				if requestCtx := <-requestContexts; requestCtx.Err() == nil {
					t.Fatal("request context not released")
				}

			})
		})
	}
}

func TestStreamingIdleTracksBytesWithinOneSSELine(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		fragments := []string{"data: ", "fragmented ", "event", "\n\n"}
		transport := HTTPTransport{
			Client: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     make(http.Header),
					Body: idleTestBody{read: func(p []byte) (int, error) {
						if len(fragments) == 0 {
							return 0, io.EOF
						}
						if err := waitForResponseBytes(req.Context(), 4*time.Minute); err != nil {
							return 0, err
						}
						n := copy(p, fragments[0])
						fragments = fragments[1:]
						return n, nil
					}},
				}, nil
			})},
		}
		resp, err := transport.StreamingDo(
			t.Context(),
			"https://example.test/respond",
			[]byte(`{}`),
			nil,
			ServerSentEventsMediaType,
		)
		require.NoError(t, err)
		defer resp.Body.Close()
		var events []SSEEvent
		err = ReadSSEEvents(
			t.Context(),
			resp.Body,
			func(event SSEEvent) error { events = append(events, event); return nil },
		)
		if err != nil || len(events) != 1 || events[0].Data != "fragmented event" {
			t.Fatalf("events=%v error=%v", events, err)
		}
	})
}

func TestIdleBodyPreservesBytesAndErrors(t *testing.T) {
	for _, cause := range []error{io.EOF, io.ErrUnexpectedEOF} {
		t.Run(cause.Error(), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithCancelCause(t.Context())
				body := &idleResponseBody{
					ctx:        ctx,
					cancel:     cancel,
					timeout:    time.Minute,
					ReadCloser: idleTestBody{read: func(p []byte) (int, error) { return copy(p, "last"), cause }},
				}
				var buffer [16]byte
				n, err := body.Read(buffer[:])
				if n != 4 || string(buffer[:n]) != "last" || !errors.Is(err, cause) {
					t.Fatalf("read=%q error=%v", buffer[:n], err)
				}
				if ctx.Err() == nil {
					t.Fatal("terminal read did not release context")
				}

				if !errors.Is(context.Cause(ctx), context.Canceled) {
					t.Fatalf("late timer replaced cleanup: %v", context.Cause(ctx))
				}
			})
		})
	}
}

func TestIdleBodyCloseUnblocksRead(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancelCause(t.Context())
		closed := false
		body := &idleResponseBody{ctx: ctx, cancel: cancel, timeout: time.Minute, ReadCloser: idleTestBody{
			read:  func([]byte) (int, error) { <-ctx.Done(); return 0, ctx.Err() },
			close: func() error { closed = true; return nil },
		}}
		done := make(chan error, 1)
		go func() { var p [1]byte; _, err := body.Read(p[:]); done <- err }()
		synctest.Wait()
		require.NoError(t, body.Close())
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Fatalf("read error=%v", err)
		}
		if !closed {
			t.Fatal("underlying body not closed")
		}

		if !errors.Is(context.Cause(ctx), context.Canceled) {
			t.Fatalf("cause=%v", context.Cause(ctx))
		}
	})
}

func TestProviderIdleWatchJoinsExpiration(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancelCause(t.Context())
		defer cancel(nil)
		started, release, stopped := make(chan struct{}), make(chan struct{}), make(chan struct{})
		stop := watchProviderIdle(func(cause error) { close(started); <-release; cancel(cause) }, time.Minute)
		<-started
		go func() { stop(); close(stopped) }()
		synctest.Wait()
		stoppedEarly := false
		select {
		case <-stopped:
			stoppedEarly = true
		default:
		}
		close(release)
		<-stopped
		if stoppedEarly {
			t.Fatal("stop returned while the expiration callback was still running")
		}
		if !errors.Is(context.Cause(ctx), errProviderIdleTimeout) {
			t.Fatalf("cause=%v", context.Cause(ctx))
		}
	})
}

type idleTestProtocol struct{ fakeProtocol }

func (*idleTestProtocol) ConsumeStream(
	ctx context.Context,
	body io.Reader,
	_ int,
	_ http.Header,
	_ model.StreamSink,
) (model.Response, error) {
	err := ReadSSEEvents(ctx, body, func(SSEEvent) error { return nil })
	if err != nil {
		return model.Response{}, model.AmbiguousProviderOutcome(
			model.ProviderError{Kind: model.ErrorKindTransient, Cause: err, Message: err.Error()},
		)
	}
	return model.Response{}, nil
}

func TestIdleTimeoutClassification(t *testing.T) {
	for _, tc := range []struct {
		name, contentType string
		status            int
		headers           bool
		kind              model.ErrorKind
		code              string
		ambiguous         bool
	}{
		{"headers", "", 200, true, model.ErrorKindTransient, "provider_idle_timeout", true},
		{"SSE", ServerSentEventsMediaType, 200, false, model.ErrorKindTransient, "provider_idle_timeout", true},
		{"JSON", "application/json", 200, false, model.ErrorKindTransient, "provider_idle_timeout", true},
		{
			"authentication error body", "application/json", 401, false, model.ErrorKindAuth,
			providerErrorCodeBodyReadFailed, false,
		},
		{
			"rate limit error body", "application/json", 429, false, model.ErrorKindRateLimit,
			providerErrorCodeBodyReadFailed, false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				client := Client{
					Endpoint: StaticEndpoint{BaseURL: "https://example.test", Path: "/respond"},
					Protocol: &idleTestProtocol{},
					Transport: HTTPTransport{
						Client: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
							if tc.headers {
								<-req.Context().Done()
								return nil, req.Context().Err()
							}
							return &http.Response{
								StatusCode: tc.status,
								Header: http.Header{
									"Content-Type": {tc.contentType},
									"X-Request-Id": {"request-test"},
									"Retry-After":  {"17"},
								},
								Body: idleTestBody{
									read: func([]byte) (int, error) { <-req.Context().Done(); return 0, req.Context().Err() },
								},
							}, nil
						})},
					},
				}
				_, err := client.RespondStream(t.Context(), model.Request{ProviderRequest: []byte(`{}`)})
				classified, ok := model.ClassifyError(err)
				if !ok || classified.Kind != tc.kind || classified.Code != tc.code ||
					!errors.Is(err, context.DeadlineExceeded) ||
					errors.Is(err, context.Canceled) {
					t.Fatalf("classification=%+v error=%v", classified, err)
				}
				if model.IsAmbiguousProviderOutcome(err) != tc.ambiguous {
					t.Fatalf("ambiguous=%v", err)
				}
				if !tc.headers &&
					(classified.RequestID != "request-test" || classified.RetryAfter == nil ||
						classified.RetryAfter.DeltaSeconds == nil || *classified.RetryAfter.DeltaSeconds != 17) {
					t.Fatalf("lost headers: %+v", classified)
				}
			})
		})
	}
}

func TestProviderTimeoutDefaultsAndCustomClient(t *testing.T) {
	if got := (HTTPTransport{}).httpClient().Timeout; got != time.Duration(
		modelstore.DefaultModelProviderRequestTimeoutMS,
	)*time.Millisecond {
		t.Fatalf("default total=%s", got)
	}
	if defaultProviderIdleTimeout != time.Duration(modelstore.DefaultModelProviderIdleTimeoutMS)*time.Millisecond {
		t.Fatal("route and stored idle defaults differ")
	}
	custom := &http.Client{Timeout: 42 * time.Second, Transport: &http.Transport{}}
	cloned := (HTTPTransport{Client: custom}).httpClient()
	if cloned == custom || cloned.Timeout != custom.Timeout || cloned.Transport != custom.Transport {
		t.Fatalf("custom client was not preserved: %+v", cloned)
	}
}

func TestIdleTimeoutIsolatesHTTP2Streams(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	idleDone := make(chan struct{})
	connections := make(chan string, 3)
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		connections <- r.RemoteAddr
		if r.ProtoMajor != 2 {
			t.Errorf("protocol=%s", r.Proto)
		}
		w.Header().Set("Content-Type", ServerSentEventsMediaType)
		w.WriteHeader(http.StatusOK)
		_ = http.NewResponseController(w).Flush()
		switch r.URL.Path {
		case "/idle":
			<-r.Context().Done()
		case "/healthy":
			ticker := time.NewTicker(20 * time.Millisecond)
			defer ticker.Stop()
			for {
				select {
				case <-idleDone:
					_, _ = io.WriteString(w, "data: complete\n\n")
					return
				case <-ticker.C:
					if _, err := io.WriteString(w, ": heartbeat\n\n"); err != nil {
						return
					}
					_ = http.NewResponseController(w).Flush()
				case <-r.Context().Done():
					return
				}
			}
		default:
			_, _ = io.WriteString(w, "data: reused\n\n")
		}
	}))
	server.EnableHTTP2 = true
	server.StartTLS()
	defer server.Close()
	transport := HTTPTransport{Client: server.Client(), IdleTimeout: time.Second}
	idle, err := transport.StreamingDo(ctx, server.URL+"/idle", []byte(`{}`), nil, ServerSentEventsMediaType)
	require.NoError(t, err)
	defer idle.Body.Close()
	healthy, err := transport.StreamingDo(ctx, server.URL+"/healthy", []byte(`{}`), nil, ServerSentEventsMediaType)
	require.NoError(t, err)
	defer healthy.Body.Close()
	idleError := make(chan error, 1)
	go func() { _, err := io.ReadAll(idle.Body); idleError <- err; close(idleDone) }()
	data, err := io.ReadAll(healthy.Body)
	if err != nil || !strings.Contains(string(data), "data: complete") {
		t.Fatalf("healthy body=%q error=%v", data, err)
	}
	if err := <-idleError; !errors.Is(err, errProviderIdleTimeout) {
		t.Fatalf("idle error=%v", err)
	}
	reused, err := transport.StreamingDo(ctx, server.URL+"/reused", []byte(`{}`), nil, ServerSentEventsMediaType)
	require.NoError(t, err)
	data, _, err = ReadAllAndClose(reused, 100)
	if err != nil || string(data) != "data: reused\n\n" {
		t.Fatalf("reused body=%q error=%v", data, err)
	}
	first := <-connections
	for range 2 {
		if next := <-connections; next != first {
			t.Fatalf("expected one shared HTTP/2 connection: %q != %q", next, first)
		}
	}
}

func TestIdleExpirationPreservesEarlierParentCancellation(t *testing.T) {
	for _, headers := range []bool{false, true} {
		name := "body"
		if headers {
			name = "headers"
		}
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				timer := time.AfterFunc(time.Second, cancel)
				defer timer.Stop()
				finishCanceledRead := func(ctx context.Context) error {
					<-ctx.Done()
					// Simulate a transport completing cleanup after the idle deadline.
					<-time.After(4 * time.Second)
					return ctx.Err()
				}
				roundTrip := func(req *http.Request) (*http.Response, error) {
					if headers {
						return nil, finishCanceledRead(req.Context())
					}
					body := idleTestBody{read: func([]byte) (int, error) { return 0, finishCanceledRead(req.Context()) }}
					return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: body}, nil
				}
				transport := HTTPTransport{IdleTimeout: 3 * time.Second, Client: &http.Client{Transport: roundTripFunc(roundTrip)}}
				resp, err := transport.StreamingDo(
					ctx, "https://example.test/respond", []byte(`{}`), nil, ServerSentEventsMediaType,
				)
				if err == nil {
					_, _, err = ReadAllAndClose(resp, 100)
				}
				if !errors.Is(err, context.Canceled) || errors.Is(err, errProviderIdleTimeout) {
					t.Fatalf("earlier parent cancellation was replaced: %v", err)
				}
			})
		})
	}
}
