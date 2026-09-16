package channelconnector

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/bearertoken"
)

func operationConfig(t *testing.T, endpoint string) Config {
	t.Helper()
	token, err := bearertoken.Generate(bearertoken.KindChannelConnector)
	if err != nil {
		t.Fatal(err)
	}
	return Config{ID: "gateway-a", Token: token, OperationsURL: endpoint,
		Capabilities: []Capability{{ConnectorKey: "test_connector", Provider: "slack"}}}
}

func operationRequest() OperationRequest {
	return OperationRequest{RequestID: "request-1", Kind: OperationSend,
		Capability: Capability{ConnectorKey: "test_connector", Provider: "slack"},
		Scope: OperationScope{ProjectID: "project", IntegrationAppID: "app", IntegrationInstallID: "install",
			AgentID: "agent", ChannelID: "channel"},
		Payload: json.RawMessage(`{"message":{"text":"hello"},"params":{}}`)}
}

func operationClient(t *testing.T, configs ...Config) *OperationsClient {
	t.Helper()
	client, err := NewOperationsClient(configs, nil)
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func operationContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func writeOperationResult(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(w, `{"request_id":"request-1","outcome":"completed","payload":{"publication":"draft"}}`)
}

func assertOperationError(t *testing.T, result OperationResult, err error, outcome OperationOutcome, code string) {
	t.Helper()
	var failure *OperationError
	if !errors.As(err, &failure) || failure.Outcome != outcome || failure.Code != code || result.Outcome != outcome {
		t.Fatalf("result = %+v, error = %v; want %s/%s", result, err, outcome, code)
	}
}

func TestOperationsURLNormalizationAndValidation(t *testing.T) {
	for _, value := range []string{
		"", "  ", "https://Gateway.EXAMPLE/private/operations/", "http://127.0.0.1:8123/ops", "http://[::1]:8123/ops",
	} {
		t.Run(value, func(t *testing.T) {
			config, err := normalizeConfig(operationConfig(t, "  "+value+"  "))
			if err != nil {
				t.Fatal(err)
			}
			if config.OperationsURL != strings.ToLower(strings.TrimSpace(value)) {
				t.Fatalf("normalized URL = %q", config.OperationsURL)
			}
		})
	}
	for _, value := range []string{
		"/ops", "gateway:8000/ops", "ftp://gateway/ops", "https:///ops", "https:gateway/ops",
		"http://user:secret@gateway/ops", "http://gateway/ops?secret=token", "http://gateway/ops?",
		"http://gateway/ops#token", "http://gateway/ops#", "http://gateway//ops", "http://gateway/a/../ops",
		"http://gateway/%2fops", "http://gateway/ops\\other", "http://gateway:0/ops", "http://gateway:65536/ops",
		"http://gateway:/ops", "http://gateway:bad/ops", "http://gateway/ops\nsecret",
	} {
		t.Run(value, func(t *testing.T) {
			_, err := NewAuthenticator([]Config{operationConfig(t, value)})
			if err == nil || strings.Contains(err.Error(), value) || strings.Contains(err.Error(), "secret") {
				t.Fatalf("invalid URL diagnostics = %v", err)
			}
		})
	}
}

func TestOperationsClientRejectsAmbiguousRoutes(t *testing.T) {
	base := operationConfig(t, "http://gateway/ops")
	for _, kind := range []string{"id", "token", "capability", "within connector", "normalized duplicate"} {
		t.Run(kind, func(t *testing.T) {
			other := operationConfig(t, "http://gateway-b/ops")
			other.ID = "gateway-b"
			other.Capabilities = []Capability{{ConnectorKey: "test_connector", Provider: "discord"}}
			configs := []Config{base, other}
			switch kind {
			case "id":
				configs[1].ID = base.ID
			case "token":
				configs[1].Token = base.Token
			case "capability":
				configs[1].Capabilities = base.Capabilities
			case "within connector", "normalized duplicate":
				duplicate := base.Capabilities[0]
				if kind == "normalized duplicate" {
					duplicate.Provider = " slack "
				}
				configs[0].Capabilities = append([]Capability{duplicate}, base.Capabilities...)
			}
			_, err := NewOperationsClient(configs, nil)
			if err == nil || strings.Contains(fmt.Sprintf("%+v", err), base.Token) {
				t.Fatalf("unsafe or missing error: %v", err)
			}
		})
	}
	// Authentication retains its existing dedup behavior; inbound-only grants do
	// not compete with an actual deployment-owned outbound route.
	inbound := operationConfig(t, "")
	inbound.ID = "inbound"
	if _, err := NewOperationsClient([]Config{base, inbound}, nil); err != nil {
		t.Fatal(err)
	}
}

func TestOperationsClientAuthenticatesAndUsesExactConfiguredEndpoint(t *testing.T) {
	var calls atomic.Int32
	ctx := operationContext(t)
	deadline, _ := ctx.Deadline()
	config := operationConfig(t, "")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.RequestURI() != "/private/operations/" || r.Method != http.MethodPost ||
			r.Header.Get("Authorization") != "Bearer "+config.Token || r.Header.Get("Idempotency-Key") != "" {
			t.Error("request did not preserve configured URL, method, or private authentication")
		}
		var envelope operationEnvelope
		if err := json.NewDecoder(r.Body).Decode(&envelope); err != nil {
			t.Error(err)
		}
		if !envelope.Deadline.Equal(deadline) || envelope.Scope != operationRequest().Scope ||
			envelope.Capability != operationRequest().Capability || envelope.RequestID != "request-1" {
			t.Errorf("incorrect envelope: %+v", envelope)
		}
		writeOperationResult(w)
	}))
	defer server.Close()
	config.OperationsURL = " " + server.URL + "/private/operations/ "
	client := operationClient(t, config)
	for _, kind := range []OperationKind{OperationSend, OperationRead, OperationInteraction} {
		request := operationRequest()
		request.Kind = kind
		result, err := client.Execute(ctx, request)
		if err != nil || result.Outcome != OperationCompleted || !bytes.Contains(result.Payload, []byte("draft")) {
			t.Fatalf("Execute(%s) = %+v, %v", kind, result, err)
		}
	}
	for _, capability := range []Capability{
		{ConnectorKey: "test_connector", Provider: "discord"},
		{ConnectorKey: "another", Provider: "slack"},
		{ConnectorKey: "test_connector", Provider: " slack "},
	} {
		request := operationRequest()
		request.Capability = capability
		result, err := client.Execute(ctx, request)
		assertOperationError(t, result, err, OperationFailed, "route_unavailable")
	}
	if calls.Load() != 3 {
		t.Fatalf("calls = %d", calls.Load())
	}
}

func TestOperationsClientSeparatesCapabilityPairs(t *testing.T) {
	var firstCalls, secondCalls atomic.Int32
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		firstCalls.Add(1)
		writeOperationResult(w)
	}))
	defer first.Close()
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		secondCalls.Add(1)
		writeOperationResult(w)
	}))
	defer second.Close()
	a, b := operationConfig(t, first.URL), operationConfig(t, second.URL)
	b.ID = "gateway-b"
	b.Capabilities[0].Provider = "discord"
	client := operationClient(t, a, b)
	request := operationRequest()
	request.Capability = b.Capabilities[0]
	if _, err := client.Execute(operationContext(t), request); err != nil {
		t.Fatal(err)
	}
	if firstCalls.Load() != 0 || secondCalls.Load() != 1 {
		t.Fatal("routed to wrong provider endpoint")
	}
}

func TestOperationsClientNeverFollowsRedirectsOrRetries(t *testing.T) {
	var redirected, calls atomic.Int32
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { redirected.Add(1) }))
	defer destination.Close()
	for _, status := range []int{301, 302, 307, 308, 401, 429, 500, 503, 202} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			calls.Store(0)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.Header().Set("Location", destination.URL)
				w.Header().Set("Retry-After", "0")
				w.WriteHeader(status)
			}))
			defer server.Close()
			client := operationClient(t, operationConfig(t, server.URL))
			result, err := client.Execute(operationContext(t), operationRequest())
			assertOperationError(t, result, err, OperationUnknown, "http_rejected")
			if calls.Load() != 1 || redirected.Load() != 0 {
				t.Fatal("HTTP mutation was repeated or redirected")
			}
		})
	}
}

func TestOperationsClientValidatesTerminalResponses(t *testing.T) {
	for _, test := range []struct {
		name, body string
		outcome    OperationOutcome
		code       string
	}{
		{"known failure", `{"request_id":"request-1","outcome":"failed"}`, OperationFailed, "gateway_failed"},
		{"unknown", `{"request_id":"request-1","outcome":"unknown"}`, OperationUnknown, "gateway_unknown"},
		{"wrong correlation", `{"request_id":"request-2","outcome":"completed","payload":{}}`,
			OperationUnknown, "invalid_response"},
		{"pending", `{"request_id":"request-1","outcome":"pending"}`, OperationUnknown, "invalid_response"},
		{"missing payload", `{"request_id":"request-1","outcome":"completed"}`, OperationUnknown, "invalid_response"},
		{"trailing value", `{"request_id":"request-1","outcome":"failed"} {}`, OperationUnknown, "invalid_response"},
		{"duplicate outcome", `{"request_id":"request-1","outcome":"unknown","outcome":"failed"}`,
			OperationUnknown, "invalid_response"},
		{"case alias", `{"request_id":"request-1","outcome":"unknown","OUTCOME":"failed"}`,
			OperationUnknown, "invalid_response"},
		{"duplicate nested key", `{"request_id":"request-1","outcome":"completed","payload":{"a":1,"\u0061":2}}`,
			OperationUnknown, "invalid_response"},
		{"unknown field", `{"request_id":"request-1","outcome":"completed","payload":{},"next_poll":"url"}`,
			OperationUnknown, "invalid_response"},
		{"oversized", strings.Repeat(" ", int(MaxOperationResponseBytes)+1), OperationUnknown, "invalid_response"},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, test.body)
			}))
			defer server.Close()
			result, err := operationClient(t, operationConfig(t, server.URL)).Execute(operationContext(t), operationRequest())
			assertOperationError(t, result, err, test.outcome, test.code)
		})
	}
}

func TestOperationsClientBoundsResponseIOAndCancels(t *testing.T) {
	for _, kind := range []OperationKind{OperationSend, OperationRead, OperationInteraction} {
		t.Run(string(kind), func(t *testing.T) {
			canceled := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{"request_id":`)
				if err := http.NewResponseController(w).Flush(); err != nil {
					t.Error(err)
				}
				<-r.Context().Done()
				close(canceled)
			}))
			defer server.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
			request := operationRequest()
			request.Kind = kind
			result, err := operationClient(t, operationConfig(t, server.URL)).Execute(ctx, request)
			want := OperationUnknown
			if kind == OperationRead {
				want = OperationFailed
			}
			if result.Outcome != want || !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("result = %+v, error = %v", result, err)
			}
			select {
			case <-canceled:
			case <-time.After(time.Second):
				t.Fatal("response I/O remained active after deadline")
			}
		})
	}
}

type operationRoundTripper func(*http.Request) (*http.Response, error)

func (f operationRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestOperationsErrorsDoNotExposeCredentialsOrSourceErrors(t *testing.T) {
	config := operationConfig(t, "http://gateway/ops")
	transport := operationRoundTripper(func(r *http.Request) (*http.Response, error) {
		if r.GetBody != nil || r.Header.Get("Idempotency-Key") != "" {
			t.Error("request can be replayed")
		}
		return nil, fmt.Errorf("request failed with Bearer %s", config.Token)
	})
	client, err := NewOperationsClient([]Config{config}, &http.Client{Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.Execute(operationContext(t), operationRequest())
	assertOperationError(t, result, err, OperationUnknown, "transport_failed")
	if strings.Contains(fmt.Sprintf("%+v", err), config.Token) || errors.Unwrap(err) != nil {
		t.Fatal("transport leaked provider credentials through diagnostic or cause")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"request_id":"request-1","outcome":"failed","payload":{"error":%q}}`, config.Token)
	}))
	defer server.Close()
	config.OperationsURL = server.URL
	result, err = operationClient(t, config).Execute(operationContext(t), operationRequest())
	if strings.Contains(fmt.Sprintf("%+v %+v", result, err), config.Token) || len(result.Payload) != 0 {
		t.Fatal("gateway error body was exposed")
	}
}

func TestOperationsRejectUnboundedOrInvalidRequestsBeforeIO(t *testing.T) {
	var calls atomic.Int32
	transport := operationRoundTripper(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		return nil, errors.New("unexpected request")
	})
	client, err := NewOperationsClient(
		[]Config{operationConfig(t, "http://gateway/ops")}, &http.Client{Transport: transport},
	)
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.Execute(context.Background(), operationRequest())
	assertOperationError(t, result, err, OperationFailed, "deadline_required")
	canceled, cancel := context.WithTimeout(context.Background(), time.Second)
	cancel()
	result, err = client.Execute(canceled, operationRequest())
	assertOperationError(t, result, err, OperationFailed, "canceled_before_dispatch")
	for _, mutate := range []func(*OperationRequest){
		func(r *OperationRequest) { r.Kind = "delete" },
		func(r *OperationRequest) { r.RequestID = "" },
		func(r *OperationRequest) { r.RequestID = "invalid-\xff" },
		func(r *OperationRequest) { r.Scope.ChannelID = "" },
		func(r *OperationRequest) { r.Scope.ChannelID = "invalid-\xff" },
		func(r *OperationRequest) { r.Payload = json.RawMessage(`{"params":{"x":1,"x":2}}`) },
		func(r *OperationRequest) { r.Payload = json.RawMessage(`{} {}`) },
		func(r *OperationRequest) { r.Payload = json.RawMessage(`null`) },
		func(r *OperationRequest) { r.Artifacts = make([]OperationArtifact, MaxOperationArtifacts+1) },
		func(r *OperationRequest) { r.Kind = OperationRead; r.Artifacts = []OperationArtifact{{ID: "a"}} },
		func(r *OperationRequest) {
			r.Artifacts = []OperationArtifact{{ID: "a", Filename: "bad\nheader", ContentType: "text/plain"}}
		},
	} {
		request := operationRequest()
		mutate(&request)
		result, err := client.Execute(operationContext(t), request)
		assertOperationError(t, result, err, OperationFailed, "invalid_request")
	}
	if calls.Load() != 0 {
		t.Fatal("invalid request performed I/O")
	}
}

func TestOperationsStreamsTwentyArtifactsWithoutAnIngressSizeCeiling(t *testing.T) {
	for _, size := range []int64{5, 10 * 1024 * 1024, 12 * 1024 * 1024} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			var published atomic.Bool
			var received atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				reader, err := r.MultipartReader()
				if err != nil {
					t.Error(err)
					return
				}
				part, err := reader.NextPart()
				if err != nil || part.FormName() != "operation" {
					t.Error("missing operation metadata")
					return
				}
				var metadata operationEnvelope
				if err := json.NewDecoder(part).Decode(&metadata); err != nil {
					t.Error(err)
					return
				}
				if len(metadata.Artifacts) != MaxOperationArtifacts {
					t.Error("lost artifact metadata")
				}
				for i := 0; ; i++ {
					part, err := reader.NextPart()
					if errors.Is(err, io.EOF) {
						break
					}
					if err != nil {
						return
					} // Incomplete body must never publish.
					if part.FormName() != "artifact" || part.FileName() != "a.txt" || part.Header.Get("Content-Type") != "text/plain" {
						t.Error("incorrect artifact framing")
					}
					n, err := io.Copy(io.Discard, part)
					received.Add(n)
					if err != nil {
						return
					}
				}
				published.Store(true)
				writeOperationResult(w)
			}))
			defer server.Close()
			request := operationRequest()
			var closed atomic.Int32
			for i := range MaxOperationArtifacts {
				fileSize := int64(0)
				if i == 0 {
					fileSize = size
				}
				request.Artifacts = append(request.Artifacts, OperationArtifact{
					ID: fmt.Sprint(i), Filename: "a.txt", ContentType: "text/plain",
					Open: func(ctx context.Context) (io.ReadCloser, error) {
						return &countedArtifactReader{Reader: io.LimitReader(zeroArtifactReader{}, fileSize), closed: &closed}, nil
					}})
			}
			result, err := operationClient(t, operationConfig(t, server.URL)).Execute(operationContext(t), request)
			if err != nil || !published.Load() || received.Load() != size || closed.Load() != MaxOperationArtifacts {
				t.Fatalf("result=%+v err=%v published=%t received=%d closed=%d",
					result, err, published.Load(), received.Load(), closed.Load())
			}
		})
	}
}

type zeroArtifactReader struct{}

func (zeroArtifactReader) Read(p []byte) (int, error) { clear(p); return len(p), nil }

type countedArtifactReader struct {
	io.Reader
	closed *atomic.Int32
}

func (r *countedArtifactReader) Close() error { r.closed.Add(1); return nil }

type blockedArtifactReader struct {
	started chan struct{}
	closed  chan struct{}
	once    sync.Once
}

func (r *blockedArtifactReader) Read([]byte) (int, error) {
	close(r.started)
	<-r.closed
	return 0, errors.New("source interrupted")
}
func (r *blockedArtifactReader) Close() error { r.once.Do(func() { close(r.closed) }); return nil }

func TestOperationsCancellationClosesBlockedArtifactSource(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
	}))
	defer server.Close()
	source := &blockedArtifactReader{started: make(chan struct{}), closed: make(chan struct{})}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	request := operationRequest()
	request.Artifacts = []OperationArtifact{{ID: "artifact", Filename: "a.txt", ContentType: "text/plain"}}
	request.Artifacts[0].Open = func(sourceCtx context.Context) (io.ReadCloser, error) {
		got, _ := sourceCtx.Deadline()
		want, _ := ctx.Deadline()
		if !got.Equal(want) {
			t.Error("artifact opener received a different deadline")
		}
		return source, nil
	}
	client := operationClient(t, operationConfig(t, server.URL))
	done := make(chan error, 1)
	go func() { _, err := client.Execute(ctx, request); done <- err }()
	select {
	case <-source.started:
	case <-time.After(time.Second):
		t.Fatal("artifact read never started")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("canceled stream did not return")
	}
	select {
	case <-source.closed:
	default:
		t.Fatal("blocked source was left running")
	}
}
