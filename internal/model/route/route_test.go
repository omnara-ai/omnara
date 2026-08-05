package route

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/omnara-ai/omnara/internal/metrics"
	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/modelcontext"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
)

type fakeProtocol struct {
	buildBody      json.RawMessage
	seen           Response
	consumedStream bool
	streamBuilds   int
	streamAccept   string
	streamResponse string
	parseErr       error
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

type readErrorBody struct {
	read bool
}

func (b *readErrorBody) Read(p []byte) (int, error) {
	if b.read {
		return 0, io.ErrUnexpectedEOF
	}
	b.read = true
	return copy(p, `{"partial":`), nil
}

func (*readErrorBody) Close() error {
	return nil
}

type closeErrorBody struct {
	io.Reader
}

func (*closeErrorBody) Close() error {
	return errors.New("close response body")
}

func (p *fakeProtocol) APIFormat() modelprotocol.APIFormat {
	return "fake-format"
}

func (p *fakeProtocol) ModelAPIVariant() modelprotocol.APIVariant {
	return "default"
}

func (p *fakeProtocol) BuildRequest(context.Context, model.PrepareInput) (json.RawMessage, error) {
	return p.buildBody, nil
}

func (p *fakeProtocol) ProjectRenderedMedia(modelcontext.Bundle) []modelcontext.RenderedMedia {
	return nil
}

func (p *fakeProtocol) ParseResponse(_ context.Context, response Response) (model.Response, error) {
	p.seen = response
	return model.Response{
		ID: "parsed",
	}, p.parseErr
}

func (p *fakeProtocol) BuildStreamRequest(body json.RawMessage) (json.RawMessage, error) {
	p.streamBuilds++
	var decoded map[string]json.RawMessage
	if err := json.Unmarshal(body, &decoded); err != nil {
		return nil, err
	}
	decoded["stream"] = json.RawMessage(`true`)
	return json.Marshal(decoded)
}

func (p *fakeProtocol) StreamAccept() string {
	if p.streamAccept != "" {
		return p.streamAccept
	}
	return ServerSentEventsMediaType
}

func (p *fakeProtocol) IsStreamingResponse(contentType string) bool {
	expected := p.streamResponse
	if expected == "" {
		expected = ServerSentEventsMediaType
	}
	return MatchesMediaType(contentType, expected)
}

func (p *fakeProtocol) ConsumeStream(
	_ context.Context,
	body io.Reader,
	_ int,
	_ http.Header,
	_ model.StreamSink,
) (model.Response, error) {
	p.consumedStream = true
	_, _ = io.ReadAll(body)
	return model.Response{ID: "streamed"}, nil
}

func TestClientPrepareDelegatesToProtocol(t *testing.T) {
	protocol := &fakeProtocol{buildBody: json.RawMessage(`{"model":"fake","input":"prepared"}`)}
	client := Client{
		ProviderModelSlug: "m",
		Endpoint:          StaticEndpoint{BaseURL: "https://example.test", Path: "/respond"},
		Protocol:          protocol,
	}
	prepared, err := client.Prepare(context.Background(), model.PrepareInput{})
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	var body map[string]any
	if err := json.Unmarshal(prepared.Body, &body); err != nil {
		t.Fatalf("decode prepared body: %v", err)
	}
	if body["model"] != "fake" || body["input"] != "prepared" || body["stream"] != true {
		t.Fatalf("prepared wire body = %+v, want protocol fields plus stream=true", body)
	}
	if protocol.streamBuilds != 1 {
		t.Fatalf("stream request builds = %d, want exactly one during prepare", protocol.streamBuilds)
	}
	if prepared.InputTokenEstimate <= 0 {
		t.Fatalf("prepared input token estimate = %d, want positive serialized-request estimate", prepared.InputTokenEstimate)
	}
	if strings.Contains(string(prepared.Body), "secret-token") {
		t.Fatalf("prepared provider body must not include auth material: %s", prepared.Body)
	}
}

func TestClientPrepareValidatesRouteWithoutProviderIO(t *testing.T) {
	roundTrips := 0
	client := Client{
		ProviderModelSlug: "m",
		Endpoint:          StaticEndpoint{BaseURL: "https://example.test", Path: "/respond"},
		Auth: Chain{
			BearerToken{Token: "secret-token"},
			Headers{"X-Provider-Version": "2026-05-10"},
		},
		Transport: HTTPTransport{Client: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			roundTrips++
			return nil, errors.New("unexpected provider I/O")
		})}},
		Protocol: &fakeProtocol{buildBody: json.RawMessage(`{"input":"prepared"}`)},
	}
	if _, err := client.Prepare(context.Background(), model.PrepareInput{}); err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if roundTrips != 0 {
		t.Fatalf("prepare performed %d provider round trips, want 0", roundTrips)
	}
}

func TestClientPrepareRejectsDeterministicRouteErrors(t *testing.T) {
	tests := []struct {
		name     string
		endpoint Endpoint
		auth     Auth
		body     json.RawMessage
		want     string
		wantKind model.ErrorKind
	}{
		{
			name:     "endpoint",
			endpoint: StaticEndpoint{BaseURL: "api.example.test"},
			want:     "endpoint URL",
			wantKind: model.ErrorKindInvalidRequest,
		},
		{
			name:     "credential",
			endpoint: StaticEndpoint{BaseURL: "https://example.test"},
			auth:     BearerToken{},
			want:     "bearer token is required",
			wantKind: model.ErrorKindAuth,
		},
		{
			name:     "header value",
			endpoint: StaticEndpoint{BaseURL: "https://example.test"},
			auth:     Headers{"X-Test": "bad\nvalue"},
			want:     "invalid value",
			wantKind: model.ErrorKindInvalidRequest,
		},
		{
			name:     "request body",
			endpoint: StaticEndpoint{BaseURL: "https://example.test"},
			body:     json.RawMessage(`{"input":`),
			want:     "invalid JSON request",
			wantKind: model.ErrorKindInvalidRequest,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := tt.body
			if len(body) == 0 {
				body = json.RawMessage(`{"input":"x"}`)
			}
			client := Client{
				Endpoint: tt.endpoint,
				Auth:     tt.auth,
				Protocol: &fakeProtocol{buildBody: body},
			}
			_, err := client.Prepare(context.Background(), model.PrepareInput{})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("prepare error = %v, want %q", err, tt.want)
			}
			var setupErr SetupError
			if !errors.As(err, &setupErr) {
				t.Fatalf("prepare error = %T, want SetupError", err)
			}
			providerErr, ok := model.ClassifyError(err)
			if !ok || providerErr.Kind != tt.wantKind {
				t.Fatalf("setup error classification = %+v ok=%v, want %s", providerErr, ok, tt.wantKind)
			}
			if model.IsAmbiguousProviderOutcome(err) {
				t.Fatalf("preflight error is ambiguous: %v", err)
			}
		})
	}
}

func TestReadSSEEventsAcceptsLargeDataLine(t *testing.T) {
	large := strings.Repeat("x", 5*1024*1024)
	stream := "event: response.completed\n" + "data: " + large + "\n\n"

	var got []SSEEvent
	if err := ReadSSEEvents(
		context.Background(),
		strings.NewReader(stream),
		func(ev SSEEvent) error {
			got = append(got, ev)
			return nil
		},
	); err != nil {
		t.Fatalf("read sse events: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("events = %d, want 1", len(got))
	}
	if got[0].Event != "response.completed" || got[0].Data != large {
		t.Fatalf("unexpected event: name=%q data_len=%d", got[0].Event, len(got[0].Data))
	}
}

func TestReadSSEEventsDiscardsFinalEventWithoutBlankLine(t *testing.T) {
	stream := "event: message_stop\ndata: {\"ok\":true}"

	var got []SSEEvent
	if err := ReadSSEEvents(
		context.Background(),
		strings.NewReader(stream),
		func(ev SSEEvent) error {
			got = append(got, ev)
			return nil
		},
	); err != nil {
		t.Fatalf("read sse events: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("unterminated SSE event was dispatched: %+v", got)
	}
}

func TestReadSSEEventsDoesNotDispatchEventWithoutData(t *testing.T) {
	stream := "event: keepalive\n\nevent: response.completed\ndata: {\"ok\":true}\n\n"

	var got []SSEEvent
	if err := ReadSSEEvents(
		context.Background(),
		strings.NewReader(stream),
		func(ev SSEEvent) error {
			got = append(got, ev)
			return nil
		},
	); err != nil {
		t.Fatalf("read sse events: %v", err)
	}
	if len(got) != 1 || got[0].Event != "response.completed" || got[0].Data != `{"ok":true}` {
		t.Fatalf("dispatched SSE events = %+v, want only the data-bearing event", got)
	}
}

func TestReadAllAndCloseDistinguishesExactLimitFromOverflow(t *testing.T) {
	exact, exceeded, err := ReadAllAndClose(StreamingResponse{
		Body: io.NopCloser(strings.NewReader("1234")),
	}, 4)
	if err != nil || exceeded || string(exact) != "1234" {
		t.Fatalf("exact limited body = %q exceeded=%v err=%v", exact, exceeded, err)
	}
	overflow, exceeded, err := ReadAllAndClose(StreamingResponse{
		Body: io.NopCloser(strings.NewReader("12345")),
	}, 4)
	if err != nil || !exceeded || string(overflow) != "1234" {
		t.Fatalf("overflow limited body = %q exceeded=%v err=%v", overflow, exceeded, err)
	}
}

func TestClientRespondStreamSendsStoredRequestWithRouteHeaders(t *testing.T) {
	var sentBody map[string]any
	var sentBytes []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/respond" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer secret-token" || r.Header.Get("X-Provider-Version") != "2026-05-10" {
			t.Fatalf("missing route headers: %+v", r.Header)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		if err := json.Unmarshal(body, &sentBody); err != nil {
			t.Fatalf("decode sent body: %v", err)
		}
		sentBytes = body
		w.Header().Set("X-Request-Id", "req_route")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	protocol := &fakeProtocol{}
	stored := json.RawMessage(`{"b":2,"a":1,"stream":true}`)
	client := Client{
		ProviderModelSlug: "m",
		Endpoint:          StaticEndpoint{BaseURL: server.URL + "/", Path: "v1/respond"},
		Auth: Chain{
			BearerToken{Token: "secret-token"},
			Headers{"X-Provider-Version": "2026-05-10"},
		},
		Transport: HTTPTransport{Client: server.Client()},
		Protocol:  protocol,
	}
	resp, err := client.RespondStream(context.Background(), model.Request{ProviderRequest: stored})
	if err != nil {
		t.Fatalf("respond: %v", err)
	}
	if sentBody["a"] != float64(1) || sentBody["b"] != float64(2) || sentBody["stream"] != true {
		t.Fatalf("sent stream body = %+v, want exact prepared fields", sentBody)
	}
	if string(sentBytes) != string(stored) {
		t.Fatalf("sent bytes = %s, want exact prepared bytes %s", sentBytes, stored)
	}
	if protocol.streamBuilds != 0 {
		t.Fatalf("respond rebuilt the provider request %d times", protocol.streamBuilds)
	}
	if resp.ID != "parsed" || resp.ProviderRequestID != "req_route" ||
		protocol.seen.Header.Get("X-Request-Id") != "req_route" ||
		string(protocol.seen.Body) != `{"ok":true}` {
		t.Fatalf("response was not parsed from transport evidence: resp=%+v seen=%+v", resp, protocol.seen)
	}
}

func TestClientRespondStreamEnrichesProviderErrorsWithRetryDirectives(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After-Ms", "30000.25")
		w.Header().Set("X-Should-Retry", "true")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"retry explicitly"}`))
	}))
	defer server.Close()

	client := Client{
		Endpoint:  StaticEndpoint{BaseURL: server.URL},
		Transport: HTTPTransport{Client: server.Client()},
		Protocol: &fakeProtocol{parseErr: model.ProviderError{
			Kind:    model.ErrorKindInvalidRequest,
			Code:    "bad_request",
			Message: "bad request",
		}},
	}
	_, err := client.RespondStream(context.Background(), model.Request{
		ProviderRequest: json.RawMessage(`{"input":"x"}`),
	})
	providerErr, ok := model.ClassifyError(err)
	if !ok || providerErr.Retryable == nil || !*providerErr.Retryable ||
		providerErr.RetryAfter == nil || providerErr.RetryAfter.DelayMilliseconds == nil ||
		*providerErr.RetryAfter.DelayMilliseconds != 30001 {
		t.Fatalf("provider retry directives = %+v ok=%v err=%v", providerErr, ok, err)
	}
}

func TestClientRespondStreamDoesNotFollowProviderRedirect(t *testing.T) {
	firstRequests := 0
	redirectedRequests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/first":
			firstRequests++
			w.Header().Set("Location", "/second")
			w.WriteHeader(http.StatusTemporaryRedirect)
			_, _ = w.Write([]byte(`{"redirect":true}`))
		case "/second":
			redirectedRequests++
			_, _ = w.Write([]byte(`{"ok":true}`))
		default:
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
	}))
	defer server.Close()

	protocol := &fakeProtocol{}
	client := Client{
		Endpoint:  StaticEndpoint{BaseURL: server.URL, Path: "/first"},
		Transport: HTTPTransport{Client: server.Client()},
		Protocol:  protocol,
	}
	if _, err := client.RespondStream(context.Background(), model.Request{
		ProviderRequest: json.RawMessage(`{"input":"x"}`),
	}); err != nil {
		t.Fatalf("respond: %v", err)
	}
	if firstRequests != 1 || redirectedRequests != 0 {
		t.Fatalf("provider requests = first:%d redirected:%d, want 1 and 0", firstRequests, redirectedRequests)
	}
	if protocol.seen.StatusCode != http.StatusTemporaryRedirect {
		t.Fatalf("observed status = %d, want %d", protocol.seen.StatusCode, http.StatusTemporaryRedirect)
	}
}

func TestClientRespondStreamParsesNonEventStreamResponse(t *testing.T) {
	var sentBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&sentBody); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	protocol := &fakeProtocol{}
	client := Client{
		ProviderModelSlug: "m",
		Endpoint:          StaticEndpoint{BaseURL: server.URL},
		Transport:         HTTPTransport{Client: server.Client()},
		Protocol:          protocol,
	}
	resp, err := client.RespondStream(
		context.Background(),
		model.Request{
			ProviderRequest: json.RawMessage(`{"input":"x","stream":true}`),
		},
	)
	if err != nil {
		t.Fatalf("respond stream: %v", err)
	}
	if resp.ID != "parsed" {
		t.Fatalf("response id = %q, want parsed", resp.ID)
	}
	if protocol.consumedStream {
		t.Fatal("non-event-stream response should use ParseResponse, not ConsumeStream")
	}
	if string(protocol.seen.Body) != `{"ok":true}` ||
		protocol.seen.Header.Get("Content-Type") != "application/json" {
		t.Fatalf("parse response evidence = %+v", protocol.seen)
	}
	if sentBody["stream"] != true {
		t.Fatalf("stream request body = %+v, want stream true", sentBody)
	}
}

func TestClientRespondStreamUsesProtocolMediaType(t *testing.T) {
	const (
		acceptMediaType   = "application/vnd.example.eventstream"
		responseMediaType = "application/x-example.eventstream"
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Accept"); got != acceptMediaType {
			t.Fatalf("Accept = %q, want %q", got, acceptMediaType)
		}
		w.Header().Set("Content-Type", responseMediaType+"; charset=binary")
		_, _ = w.Write([]byte("stream frame"))
	}))
	defer server.Close()

	protocol := &fakeProtocol{streamAccept: acceptMediaType, streamResponse: responseMediaType}
	client := Client{
		Endpoint:  StaticEndpoint{BaseURL: server.URL},
		Transport: HTTPTransport{Client: server.Client()},
		Protocol:  protocol,
	}
	response, err := client.RespondStream(context.Background(), model.Request{
		ProviderRequest: json.RawMessage(`{"input":"x","stream":true}`),
	})
	if err != nil {
		t.Fatalf("respond custom stream: %v", err)
	}
	if !protocol.consumedStream || response.ID != "streamed" {
		t.Fatalf("custom stream response = %+v consumed=%v", response, protocol.consumedStream)
	}
}

func TestClientRespondStreamRejectsOversizedNonEventStreamResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"value":"response body is too large"}`))
	}))
	defer server.Close()

	client := Client{
		Endpoint: StaticEndpoint{BaseURL: server.URL},
		Transport: HTTPTransport{
			Client:           server.Client(),
			MaxResponseBytes: 16,
		},
		Protocol: &fakeProtocol{},
	}
	_, err := client.RespondStream(context.Background(), model.Request{
		ProviderRequest: json.RawMessage(`{"input":"x"}`),
	})
	providerErr, ok := model.ClassifyError(err)
	if !ok || providerErr.Kind != model.ErrorKindUnknown ||
		providerErr.Code != "provider_response_too_large" || !model.IsAmbiguousProviderOutcome(err) {
		t.Fatalf("oversized non-stream response = %+v ok=%v err=%v", providerErr, ok, err)
	}
}

func TestClientRespondStreamAppliesResponseLimitAfterDecompression(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Encoding", "gzip")
		w.Header().Set("Content-Type", "application/json")
		compressed := gzip.NewWriter(w)
		_, _ = compressed.Write([]byte(strings.Repeat("x", 1024)))
		_ = compressed.Close()
	}))
	defer server.Close()

	client := Client{
		Endpoint: StaticEndpoint{BaseURL: server.URL},
		Transport: HTTPTransport{
			Client:           server.Client(),
			MaxResponseBytes: 64,
		},
		Protocol: &fakeProtocol{},
	}
	_, err := client.RespondStream(context.Background(), model.Request{
		ProviderRequest: json.RawMessage(`{"input":"x"}`),
	})
	providerErr, ok := model.ClassifyError(err)
	if !ok || providerErr.Kind != model.ErrorKindUnknown ||
		providerErr.Code != "provider_response_too_large" || !model.IsAmbiguousProviderOutcome(err) {
		t.Fatalf("compressed oversized response = %+v ok=%v err=%v", providerErr, ok, err)
	}
}

func TestClientRespondStreamRejectsOversizedEventStreamResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(strings.Repeat("x", 32)))
	}))
	defer server.Close()

	client := Client{
		Endpoint: StaticEndpoint{BaseURL: server.URL},
		Transport: HTTPTransport{
			Client:           server.Client(),
			MaxResponseBytes: 16,
		},
		Protocol: &fakeProtocol{},
	}
	_, err := client.RespondStream(context.Background(), model.Request{
		ProviderRequest: json.RawMessage(`{"input":"x"}`),
	})
	providerErr, ok := model.ClassifyError(err)
	if !ok || providerErr.Kind != model.ErrorKindUnknown ||
		providerErr.Code != "provider_response_too_large" || !model.IsAmbiguousProviderOutcome(err) {
		t.Fatalf("oversized stream response = %+v ok=%v err=%v", providerErr, ok, err)
	}
}

func TestClientRespondStreamBoundsOversizedProviderErrorBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "17")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(strings.Repeat("x", 32)))
	}))
	defer server.Close()

	client := Client{
		Endpoint: StaticEndpoint{BaseURL: server.URL},
		Transport: HTTPTransport{
			Client:                server.Client(),
			MaxErrorResponseBytes: 16,
		},
		Protocol: &fakeProtocol{},
	}
	_, err := client.RespondStream(context.Background(), model.Request{
		ProviderRequest: json.RawMessage(`{"input":"x"}`),
	})
	providerErr, ok := model.ClassifyError(err)
	if !ok || providerErr.Kind != model.ErrorKindRateLimit ||
		providerErr.Code != "provider_error_body_too_large" ||
		providerErr.RetryAfter == nil || providerErr.RetryAfter.DeltaSeconds == nil ||
		*providerErr.RetryAfter.DeltaSeconds != 17 || model.IsAmbiguousProviderOutcome(err) {
		t.Fatalf("oversized error response = %+v ok=%v err=%v", providerErr, ok, err)
	}
}

func TestClientRespondStreamKeepsOversizedTransientHTTPErrorRetryable(t *testing.T) {
	for _, statusCode := range []int{http.StatusRequestTimeout, http.StatusConflict} {
		t.Run(http.StatusText(statusCode), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Retry-After", "17")
				w.WriteHeader(statusCode)
				_, _ = w.Write([]byte(strings.Repeat("x", 32)))
			}))
			defer server.Close()

			client := Client{
				Endpoint: StaticEndpoint{BaseURL: server.URL},
				Transport: HTTPTransport{
					Client:                server.Client(),
					MaxErrorResponseBytes: 16,
				},
				Protocol: &fakeProtocol{},
			}
			_, err := client.RespondStream(context.Background(), model.Request{
				ProviderRequest: json.RawMessage(`{"input":"x"}`),
			})
			providerErr, ok := model.ClassifyError(err)
			if !ok || providerErr.Kind != model.ErrorKindTransient ||
				providerErr.RetryAfter == nil || providerErr.RetryAfter.DeltaSeconds == nil ||
				*providerErr.RetryAfter.DeltaSeconds != 17 || model.IsAmbiguousProviderOutcome(err) {
				t.Fatalf("oversized transient response = %+v ok=%v err=%v", providerErr, ok, err)
			}
		})
	}
}

func TestClientRespondStreamParsesCompleteErrorJSONFromBoundedPrefix(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":"slow down"}` + strings.Repeat(" ", 32)))
	}))
	defer server.Close()

	parsedErr := model.ProviderError{
		Kind:       model.ErrorKindRateLimit,
		StatusCode: http.StatusTooManyRequests,
		Code:       "parsed_prefix",
		Message:    "slow down",
	}
	client := Client{
		Endpoint: StaticEndpoint{BaseURL: server.URL},
		Transport: HTTPTransport{
			Client:                server.Client(),
			MaxErrorResponseBytes: 24,
		},
		Protocol: &fakeProtocol{parseErr: parsedErr},
	}
	_, err := client.RespondStream(context.Background(), model.Request{
		ProviderRequest: json.RawMessage(`{"input":"x"}`),
	})
	providerErr, ok := model.ClassifyError(err)
	if !ok || providerErr.Code != "parsed_prefix" || model.IsAmbiguousProviderOutcome(err) {
		t.Fatalf("bounded parsed error prefix = %+v ok=%v err=%v", providerErr, ok, err)
	}
}

func TestClientRespondStreamLabelsHTTPMetricsWithObservedClientPathLabel(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	metricSet := metrics.New()
	recorder := metrics.NewHTTPClientRecorder(metricSet, metrics.SubsystemHTTPClient)
	httpClient := metrics.NewObservedHTTPClient(server.Client(), recorder, metrics.WithHTTPClientPathLabel("/v1/respond"))

	client := Client{
		ProviderModelSlug: "m",
		Endpoint:          StaticEndpoint{BaseURL: server.URL, Path: "/actual/provider/path"},
		Transport:         HTTPTransport{Client: httpClient},
		Protocol:          &fakeProtocol{},
	}
	body := json.RawMessage(`{"input":"x"}`)
	if _, err := client.RespondStream(context.Background(), model.Request{ProviderRequest: body}); err != nil {
		t.Fatalf("respond: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, metrics.ScrapePath, nil)
	resp := httptest.NewRecorder()
	metricSet.Handler().ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("scrape status=%d want=%d", resp.Code, http.StatusOK)
	}
	host := strings.TrimPrefix(server.URL, "http://")
	want := `omnara_http_client_requests_total{code="200",error_kind="none",host="` +
		host +
		`",method="POST",path="/v1/respond",result="success"} 1`
	if !strings.Contains(resp.Body.String(), want) {
		t.Fatalf("metrics missing %q:\n%s", want, resp.Body.String())
	}
	if strings.Contains(resp.Body.String(), "/actual/provider/path") {
		t.Fatalf("raw provider path leaked into metrics:\n%s", resp.Body.String())
	}
}

func TestClientRespondStreamClassifiesAuthSetupErrorWithoutTransportAmbiguity(t *testing.T) {
	client := Client{
		ProviderModelSlug: "m",
		Endpoint:          StaticEndpoint{BaseURL: "https://example.test"},
		Auth:              BearerToken{},
		Protocol:          &fakeProtocol{},
	}
	_, err := client.RespondStream(context.Background(), model.Request{ProviderRequest: json.RawMessage(`{"input":"x"}`)})
	if err == nil || err.Error() != "bearer token is required" {
		t.Fatalf("expected direct auth setup error, got %v", err)
	}
	providerErr, ok := model.ClassifyError(err)
	if !ok || providerErr.Kind != model.ErrorKindAuth {
		t.Fatalf("auth setup error classification = %+v ok=%v", providerErr, ok)
	}
	if model.IsAmbiguousProviderOutcome(err) {
		t.Fatalf("auth setup error is ambiguous: %v", err)
	}
}

func TestClientRespondStreamClassifiesEndpointSetupErrorWithoutTransportAmbiguity(t *testing.T) {
	tests := []struct {
		name    string
		baseURL string
		path    string
	}{
		{name: "missing scheme with path", baseURL: "api.openai.com/v1", path: "/respond"},
		{name: "unsupported scheme with path", baseURL: "ftp://example.test", path: "/respond"},
		{name: "missing scheme without path", baseURL: "api.openai.com/v1"},
		{name: "missing host without path", baseURL: "https://"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := Client{
				ProviderModelSlug: "m",
				Endpoint:          StaticEndpoint{BaseURL: tt.baseURL, Path: tt.path},
				Protocol:          &fakeProtocol{},
			}
			_, err := client.RespondStream(context.Background(), model.Request{ProviderRequest: json.RawMessage(`{"input":"x"}`)})
			if err == nil || !strings.Contains(err.Error(), "model endpoint URL") {
				t.Fatalf("expected endpoint setup error, got %v", err)
			}
			providerErr, ok := model.ClassifyError(err)
			if !ok || providerErr.Kind != model.ErrorKindInvalidRequest {
				t.Fatalf("endpoint setup error classification = %+v ok=%v", providerErr, ok)
			}
			if model.IsAmbiguousProviderOutcome(err) {
				t.Fatalf("endpoint setup error is ambiguous: %v", err)
			}
		})
	}
}

func TestClientRespondStreamClassifiesNetworkSendFailureAsTransient(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatalf("close listener: %v", err)
	}
	endpoint := "http://" + addr + "/respond"
	client := Client{
		ProviderModelSlug: "m",
		Endpoint:          StaticEndpoint{BaseURL: "http://" + addr, Path: "/respond"},
		Protocol:          &fakeProtocol{},
	}
	_, err = client.RespondStream(context.Background(), model.Request{ProviderRequest: json.RawMessage(`{"input":"x"}`)})
	providerErr, ok := model.ClassifyError(err)
	if !ok || providerErr.Kind != model.ErrorKindTransient || providerErr.Source != endpoint {
		t.Fatalf("expected transient provider error for network send failure, got %+v ok=%v err=%v", providerErr, ok, err)
	}
	if !model.IsAmbiguousProviderOutcome(err) {
		t.Fatalf("network send failure = %T %v, want ambiguous outcome", err, err)
	}
}

func TestClientRespondStreamPreservesCanceledContext(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer server.Close()

	client := Client{
		ProviderModelSlug: "m",
		Endpoint:          StaticEndpoint{BaseURL: server.URL, Path: "/respond"},
		Transport:         HTTPTransport{Client: server.Client()},
		Protocol:          &fakeProtocol{},
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := client.RespondStream(ctx, model.Request{ProviderRequest: json.RawMessage(`{"input":"x"}`)})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("respond error = %v, want context.Canceled", err)
	}
	if !model.IsAmbiguousProviderOutcome(err) {
		t.Fatalf("canceled request = %T %v, want ambiguous outcome", err, err)
	}
	if providerErr, ok := model.ClassifyError(err); !ok || providerErr.Kind != model.ErrorKindTransient {
		t.Fatalf("canceled request classification = %+v ok=%v", providerErr, ok)
	}
}

func TestClientRespondStreamClassifiesUnreadableErrorBodyFromHTTPStatus(t *testing.T) {
	tests := []struct {
		statusCode int
		wantKind   model.ErrorKind
	}{
		{statusCode: http.StatusBadRequest, wantKind: model.ErrorKindInvalidRequest},
		{statusCode: http.StatusUnauthorized, wantKind: model.ErrorKindAuth},
		{statusCode: http.StatusTooManyRequests, wantKind: model.ErrorKindRateLimit},
		{statusCode: http.StatusServiceUnavailable, wantKind: model.ErrorKindProviderUnavailable},
	}
	for _, tt := range tests {
		t.Run(http.StatusText(tt.statusCode), func(t *testing.T) {
			roundTrips := 0
			client := Client{
				Endpoint: StaticEndpoint{BaseURL: "https://example.test", Path: "/respond"},
				Transport: HTTPTransport{Client: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
					roundTrips++
					return &http.Response{
						StatusCode: tt.statusCode,
						Header: http.Header{
							"X-Request-Id":   []string{"req_read"},
							"Retry-After":    []string{"23"},
							"X-Should-Retry": []string{"false"},
						},
						Body: &readErrorBody{},
					}, nil
				})}},
				Protocol: &fakeProtocol{},
			}
			_, err := client.RespondStream(context.Background(), model.Request{
				ProviderRequest: json.RawMessage(`{"input":"x"}`),
			})
			providerErr, ok := model.ClassifyError(err)
			if !ok || providerErr.Kind != tt.wantKind || providerErr.StatusCode != tt.statusCode ||
				providerErr.Code != "provider_error_body_read_failed" ||
				providerErr.RequestID != "req_read" || providerErr.RetryAfter == nil ||
				providerErr.RetryAfter.DeltaSeconds == nil ||
				*providerErr.RetryAfter.DeltaSeconds != 23 || providerErr.Retryable == nil ||
				*providerErr.Retryable {
				t.Fatalf("body read provider evidence = %+v ok=%v err=%v", providerErr, ok, err)
			}
			if model.IsAmbiguousProviderOutcome(err) {
				t.Fatalf("known HTTP error status became ambiguous: %v", err)
			}
			if !errors.Is(err, io.ErrUnexpectedEOF) {
				t.Fatalf("body read cause was lost: %v", err)
			}
			if roundTrips != 1 {
				t.Fatalf("provider round trips = %d, want exactly 1", roundTrips)
			}
		})
	}
}

func TestClientRespondStreamParsesFullyReadBodyDespiteCloseError(t *testing.T) {
	protocol := &fakeProtocol{}
	client := Client{
		Endpoint: StaticEndpoint{BaseURL: "https://example.test", Path: "/respond"},
		Transport: HTTPTransport{Client: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       &closeErrorBody{Reader: strings.NewReader(`{"ok":true}`)},
			}, nil
		})}},
		Protocol: protocol,
	}
	resp, err := client.RespondStream(context.Background(), model.Request{
		ProviderRequest: json.RawMessage(`{"input":"x"}`),
	})
	if err != nil {
		t.Fatalf("fully read response: %v", err)
	}
	if resp.ID != "parsed" || string(protocol.seen.Body) != `{"ok":true}` {
		t.Fatalf("parsed response = %+v seen=%+v", resp, protocol.seen)
	}
}
