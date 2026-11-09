package model_test

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/model/route"
	"github.com/omnara-ai/omnara/observability/metrics"
	log "github.com/omnara-ai/omnara/observability/wideevent"
)

// Frames arrive at 1s (metadata), 2s (first token), and 3s (terminal); usage is 100 input, 40 cached.
var callTraceStreams = map[string][3]string{
	"chat": {
		": OPENROUTER PROCESSING\n\n" +
			`data: {"id":"c1","model":"test-model","choices":[{"index":0,"delta":{"role":"assistant"}}]}` + "\n\n",
		`data: {"id":"c1","model":"test-model","choices":[{"index":0,"delta":{"content":"Hello"}}]}` + "\n\n",
		`data: {"id":"c1","model":"test-model","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}` + "\n\n" +
			`data: {"id":"c1","model":"test-model","choices":[],"usage":{"prompt_tokens":100,"completion_tokens":5,` +
			`"prompt_tokens_details":{"cached_tokens":40}}}` + "\n\n" +
			"data: [DONE]\n\n",
	},
	"responses": {
		sseFrame("response.created", `{"type":"response.created","response":{"id":"r1","status":"in_progress"}}`),
		sseFrame("response.output_item.added", `{"type":"response.output_item.added","item":{"id":"m1","type":"message"}}`) +
			sseFrame("response.content_part.added", `{"type":"response.content_part.added","item_id":"m1",`+
				`"content_index":0,"part":{"type":"output_text"}}`) +
			sseFrame("response.output_text.delta", `{"type":"response.output_text.delta","item_id":"m1",`+
				`"content_index":0,"delta":"Hello"}`),
		sseFrame("response.completed", `{"type":"response.completed","response":{"id":"r1","model":"test-model",`+
			`"status":"completed","output":[{"id":"m1","type":"message","content":[{"type":"output_text","text":"Hello"}]}],`+
			`"usage":{"input_tokens":100,"output_tokens":5,"input_tokens_details":{"cached_tokens":40}}}}`),
	},
	"anthropic": {
		sseFrame("message_start", `{"type":"message_start","message":{"id":"m1","model":"test-model",`+
			`"usage":{"input_tokens":60,"cache_read_input_tokens":40}}}`) +
			sseFrame("ping", `{"type":"ping"}`),
		sseFrame("content_block_start", `{"type":"content_block_start","index":0,`+
			`"content_block":{"type":"text","text":""}}`) +
			sseFrame("content_block_delta", `{"type":"content_block_delta","index":0,`+
				`"delta":{"type":"text_delta","text":"Hello"}}`),
		sseFrame("content_block_stop", `{"type":"content_block_stop","index":0}`) +
			sseFrame("message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn"},`+
				`"usage":{"output_tokens":5}}`) +
			sseFrame("message_stop", `{"type":"message_stop"}`),
	},
}

// Frames arrive at 1s (metadata), 2s (tool name), and 3s (arguments).
var callTraceToolStreams = map[string][3]string{
	"chat": {
		`data: {"id":"c1","model":"test-model","choices":[{"index":0,"delta":{"role":"assistant"}}]}` + "\n\n",
		`data: {"id":"c1","model":"test-model","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,` +
			`"id":"call_1","type":"function","function":{"name":"lookup","arguments":""}}]}}]}` + "\n\n",
		`data: {"id":"c1","model":"test-model","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,` +
			`"function":{"arguments":"{\"q\":1}"}}]},"finish_reason":"tool_calls"}]}` + "\n\n" +
			"data: [DONE]\n\n",
	},
	"responses": {
		sseFrame("response.created", `{"type":"response.created","response":{"id":"r1","status":"in_progress"}}`),
		sseFrame("response.output_item.added", `{"type":"response.output_item.added","item":{"id":"fc1",`+
			`"type":"function_call","call_id":"call_1","name":"lookup","arguments":""}}`),
		sseFrame("response.function_call_arguments.delta", `{"type":"response.function_call_arguments.delta",`+
			`"item_id":"fc1","delta":"{\"q\":1}"}`) +
			sseFrame("response.completed", `{"type":"response.completed","response":{"id":"r1","model":"test-model",`+
				`"status":"completed","output":[{"id":"fc1","type":"function_call","call_id":"call_1",`+
				`"name":"lookup","arguments":"{\"q\":1}"}]}}`),
	},
	"anthropic": {
		sseFrame("message_start", `{"type":"message_start","message":{"id":"m1","model":"test-model"}}`),
		sseFrame("content_block_start", `{"type":"content_block_start","index":0,`+
			`"content_block":{"type":"tool_use","id":"toolu_1","name":"lookup","input":{}}}`) +
			sseFrame("content_block_delta", `{"type":"content_block_delta","index":0,`+
				`"delta":{"type":"input_json_delta","partial_json":""}}`),
		sseFrame("content_block_delta", `{"type":"content_block_delta","index":0,`+
			`"delta":{"type":"input_json_delta","partial_json":"{\"q\":1}"}}`) +
			sseFrame("content_block_stop", `{"type":"content_block_stop","index":0}`) +
			sseFrame("message_delta", `{"type":"message_delta","delta":{"stop_reason":"tool_use"}}`) +
			sseFrame("message_stop", `{"type":"message_stop"}`),
	},
}

func sseFrame(event, data string) string {
	return "event: " + event + "\ndata: " + data + "\n\n"
}

// scriptedTransport returns headers at 500ms.
func scriptedTransport(frames [3]string) http.RoundTripper {
	return timeoutRoundTrip(func(req *http.Request) (*http.Response, error) {
		_, _ = io.Copy(io.Discard, req.Body)
		time.Sleep(500 * time.Millisecond) //nolint:omnaralint // Advance synctest's virtual clock.
		reader, writer := io.Pipe()
		go func() {
			for i, frame := range frames {
				//nolint:omnaralint // Advance synctest's virtual clock.
				time.Sleep([]time.Duration{500 * time.Millisecond, time.Second, time.Second}[i])
				if _, err := io.WriteString(writer, frame); err != nil {
					return
				}
			}
			_ = writer.Close()
		}()
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": {"text/event-stream"}, "X-Request-Id": {"req_provider_1"}},
			Body:       reader,
		}, nil
	})
}

func modelCallEvents(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var events []map[string]any
	scanner := bufio.NewScanner(buf)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
	for scanner.Scan() {
		var event map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			t.Fatalf("decode log line %q: %v", scanner.Text(), err)
		}
		if event["event.name"] == "model.call" {
			events = append(events, event)
		}
	}
	return events
}

func scrapeModelCallMetrics(t *testing.T, set *metrics.Set) string {
	t.Helper()
	recorder := httptest.NewRecorder()
	set.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	return recorder.Body.String()
}

func TestRespondEmitsOneModelCallRecordPerAttemptWithPhaseTimings(t *testing.T) {
	body := []byte(`{"model":"test-model","input":"secret prompt"}`)
	for name, frames := range callTraceStreams {
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				var buf bytes.Buffer
				set := metrics.New()
				ctx := log.WithLogger(t.Context(), slog.New(log.NewJSONHandler(&buf, nil)))
				ctx = route.WithModelCallMetrics(ctx, metrics.NewModelCallRecorder(set))
				ctx = model.WithCallScope(ctx, model.CallScope{
					Purpose: model.CallPurposeAgentTurn, ModelCallContextID: "ctx-1", Attempt: 2,
				})
				client := clientNamed(t, name, &http.Client{Transport: scriptedTransport(frames)})

				response, err := client.Respond(ctx, model.Request{ProviderRequest: body})
				if err != nil {
					t.Fatalf("respond: %v", err)
				}
				if response.Text() != "Hello" {
					t.Fatalf("text = %q", response.Text())
				}

				events := modelCallEvents(t, &buf)
				if len(events) != 1 {
					t.Fatalf("model.call events = %d, want 1", len(events))
				}
				event := events[0]
				for field, want := range map[string]any{
					"model_call.outcome":               "success",
					"model_call.purpose":               "agent_turn",
					"model_call.attempt":               float64(2),
					"model_call.model_call_context_id": "ctx-1",
					"model_call.model":                 "test-model",
					"model_call.endpoint_host":         "example.test",
					"model_call.streamed":              true,
					"model_call.status_code":           float64(200),
					"model_call.provider_request_id":   "req_provider_1",
					"model_call.request_bytes":         float64(len(body)),
					"model_call.usage.input_tokens":    float64(100),
					"model_call.usage.cached_tokens":   float64(40),
					"model_call.usage.output_tokens":   float64(5),
					"model_call.cache_ratio":           0.4,
					"model_call.headers_ms":            float64(500),
					"model_call.ttft_ms":               float64(2000),
					"model_call.stream_complete_ms":    float64(3000),
					"model_call.duration_ms":           float64(3000),
				} {
					if event[field] != want {
						t.Errorf("%s = %#v, want %#v", field, event[field], want)
					}
				}
				line, _ := json.Marshal(event)
				for _, banned := range []string{"Hello", "secret prompt"} {
					if strings.Contains(string(line), banned) {
						t.Fatalf("model.call event leaked content %q: %s", banned, line)
					}
				}

				scraped := scrapeModelCallMetrics(t, set)
				for _, want := range []string{
					`omnara_model_call_attempts_total{api_format="` + string(client.APIFormat()) +
						`",api_variant="default",error_kind="",model="test-model",outcome="success",` +
						`provider="example.test",purpose="agent_turn"} 1`,
					`omnara_model_call_ttft_seconds_sum{api_format="` + string(client.APIFormat()) +
						`",api_variant="default",model="test-model",provider="example.test"} 2`,
					`omnara_model_call_input_tokens_total{api_format="` + string(client.APIFormat()) +
						`",api_variant="default",model="test-model",provider="example.test"} 100`,
				} {
					if !strings.Contains(scraped, want) {
						t.Errorf("metrics missing %q", want)
					}
				}
				if strings.Contains(scraped, "ctx-1") || strings.Contains(scraped, "req_provider_1") {
					t.Fatalf("metrics carry call IDs:\n%s", scraped)
				}
			})
		})
	}
}

func TestRespondRecordsFailedAttemptWithNullTokenTimings(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var buf bytes.Buffer
		ctx := log.WithLogger(t.Context(), slog.New(log.NewJSONHandler(&buf, nil)))
		transport := timeoutRoundTrip(func(*http.Request) (*http.Response, error) {
			time.Sleep(250 * time.Millisecond) //nolint:omnaralint // Advance synctest's virtual clock.
			return &http.Response{
				StatusCode: http.StatusTooManyRequests,
				Header:     http.Header{"Content-Type": {"application/json"}},
				Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"slow down","type":"rate_limit_error"}}`)),
			}, nil
		})
		client := clientNamed(t, "chat", &http.Client{Transport: transport})
		for range 2 {
			if _, err := client.Respond(ctx, model.Request{ProviderRequest: []byte(`{}`)}); err == nil {
				t.Fatal("expected provider error")
			}
		}

		events := modelCallEvents(t, &buf)
		if len(events) != 2 || events[0]["model_call.id"] == events[1]["model_call.id"] {
			t.Fatalf("want two attempts with distinct ids, got %+v", events)
		}
		event := events[0]
		if event["model_call.outcome"] != "error" || event["model_call.error_kind"] != string(model.ErrorKindRateLimit) ||
			event["model_call.status_code"] != float64(429) || event["model_call.headers_ms"] != float64(250) ||
			event["level"] != "warn" || event["model_call.purpose"] != "unknown" {
			t.Fatalf("event = %+v", event)
		}
		for _, field := range []string{
			"model_call.ttft_ms", "model_call.stream_complete_ms",
			"model_call.usage.input_tokens",
		} {
			value, ok := event[field]
			if !ok || value != nil {
				t.Errorf("%s = %#v (present %v), want null", field, value, ok)
			}
		}
	})
}

func TestRespondTTFTCountsToolCallName(t *testing.T) {
	for name, frames := range callTraceToolStreams {
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				var buf bytes.Buffer
				ctx := log.WithLogger(t.Context(), slog.New(log.NewJSONHandler(&buf, nil)))
				client := clientNamed(t, name, &http.Client{Transport: scriptedTransport(frames)})
				if _, err := client.Respond(ctx, model.Request{ProviderRequest: []byte(`{}`)}); err != nil {
					t.Fatalf("respond: %v", err)
				}
				events := modelCallEvents(t, &buf)
				if len(events) != 1 {
					t.Fatalf("events = %d", len(events))
				}
				event := events[0]
				if event["model_call.ttft_ms"] != float64(2000) {
					t.Fatalf("ttft = %#v", event["model_call.ttft_ms"])
				}
				if line, _ := json.Marshal(event); strings.Contains(string(line), "lookup") {
					t.Fatalf("model.call event leaked the tool name: %s", line)
				}
			})
		})
	}
}

func TestRespondTTFTCountsToolArgumentsBeforeToolMetadata(t *testing.T) {
	frames := [3]string{
		`data: {"id":"c1","model":"test-model","choices":[{"index":0,"delta":{"role":"assistant"}}]}` + "\n\n",
		`data: {"id":"c1","model":"test-model","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,` +
			`"function":{"arguments":"{\"q\":"}}]}}]}` + "\n\n",
		`data: {"id":"c1","model":"test-model","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,` +
			`"id":"call_1","type":"function","function":{"name":"lookup","arguments":"1}"}}]},` +
			`"finish_reason":"tool_calls"}]}` + "\n\n" + "data: [DONE]\n\n",
	}
	synctest.Test(t, func(t *testing.T) {
		event, _ := respondOnce(t, "chat", frames)
		if event["model_call.ttft_ms"] != float64(2000) {
			t.Fatalf("ttft = %#v, want the arrival of the buffered arguments", event["model_call.ttft_ms"])
		}
	})
}

func TestRespondTTFTCountsWhitespaceTokens(t *testing.T) {
	var frames [3]string
	for i, frame := range callTraceStreams["chat"] {
		frames[i] = strings.Replace(frame, `"content":"Hello"`, `"content":"\n"`, 1)
	}
	synctest.Test(t, func(t *testing.T) {
		if event, _ := respondOnce(t, "chat", frames); event["model_call.ttft_ms"] != float64(2000) {
			t.Fatalf("ttft = %#v", event["model_call.ttft_ms"])
		}
	})
}

func TestRespondCacheRatioNeedsReportedCacheCount(t *testing.T) {
	reported := map[string][2]string{
		"chat":      {`,"prompt_tokens_details":{"cached_tokens":40}`, `,"prompt_tokens_details":{"cached_tokens":0}`},
		"responses": {`,"input_tokens_details":{"cached_tokens":40}`, `,"input_tokens_details":{"cached_tokens":0}`},
		"anthropic": {`"input_tokens":60,"cache_read_input_tokens":40`, `"input_tokens":100,"cache_read_input_tokens":0`},
	}
	omitted := map[string]string{"chat": "", "responses": "", "anthropic": `"input_tokens":100`}
	for name, usage := range reported {
		for _, tc := range []struct {
			name, usage   string
			ratio, cached any
			measuredInput string
		}{
			{"omitted", omitted[name], nil, nil, "0"},
			{"zero", usage[1], float64(0), float64(0), "100"},
		} {
			t.Run(name+"/"+tc.name, func(t *testing.T) {
				var frames [3]string
				for i, frame := range callTraceStreams[name] {
					frames[i] = strings.Replace(frame, usage[0], tc.usage, 1)
				}
				synctest.Test(t, func(t *testing.T) {
					event, scraped := respondOnce(t, name, frames)
					if event["model_call.usage.input_tokens"] != float64(100) ||
						event["model_call.cache_ratio"] != tc.ratio || event["model_call.usage.cached_tokens"] != tc.cached {
						t.Fatalf("input = %#v ratio = %#v cached = %#v", event["model_call.usage.input_tokens"],
							event["model_call.cache_ratio"], event["model_call.usage.cached_tokens"])
					}
					if !strings.Contains(scraped, `omnara_model_call_cache_reported_input_tokens_total{`) ||
						!strings.Contains(scraped, `provider="example.test"} `+tc.measuredInput+"\n") {
						t.Fatalf("cache_reported_input_tokens_total != %s:\n%s", tc.measuredInput, scraped)
					}
				})
			})
		}
	}
}

func respondOnce(t *testing.T, name string, frames [3]string) (map[string]any, string) {
	t.Helper()
	var buf bytes.Buffer
	set := metrics.New()
	ctx := log.WithLogger(t.Context(), slog.New(log.NewJSONHandler(&buf, nil)))
	ctx = route.WithModelCallMetrics(ctx, metrics.NewModelCallRecorder(set))
	client := clientNamed(t, name, &http.Client{Transport: scriptedTransport(frames)})
	if _, err := client.Respond(ctx, model.Request{ProviderRequest: []byte(`{}`)}); err != nil {
		t.Fatalf("respond: %v", err)
	}
	events := modelCallEvents(t, &buf)
	if len(events) != 1 {
		t.Fatalf("events = %d", len(events))
	}
	return events[0], scrapeModelCallMetrics(t, set)
}

func TestRespondLeavesTTFTNullForNonStreamedBody(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var buf bytes.Buffer
		ctx := log.WithLogger(t.Context(), slog.New(log.NewJSONHandler(&buf, nil)))
		transport := timeoutRoundTrip(func(*http.Request) (*http.Response, error) {
			time.Sleep(time.Second) //nolint:omnaralint // Advance synctest's virtual clock.
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": {"application/json"}},
				Body: io.NopCloser(strings.NewReader(`{"id":"c1","model":"test-model","choices":[{"index":0,` +
					`"message":{"role":"assistant","content":"Hello"},"finish_reason":"stop"}]}`)),
			}, nil
		})
		client := clientNamed(t, "chat", &http.Client{Transport: transport})
		if _, err := client.Respond(ctx, model.Request{ProviderRequest: []byte(`{}`)}); err != nil {
			t.Fatalf("respond: %v", err)
		}
		events := modelCallEvents(t, &buf)
		if len(events) != 1 {
			t.Fatalf("events = %d", len(events))
		}
		event := events[0]
		if event["model_call.streamed"] != false || event["model_call.ttft_ms"] != nil ||
			event["model_call.stream_complete_ms"] != float64(1000) {
			t.Fatalf("event = %+v", event)
		}
	})
}

func TestRespondSkipsRecordWhenRequestIsNeverSent(t *testing.T) {
	var buf bytes.Buffer
	ctx := log.WithLogger(t.Context(), slog.New(log.NewJSONHandler(&buf, nil)))
	client := clientNamed(t, "chat", &http.Client{Transport: timeoutRoundTrip(func(*http.Request) (*http.Response, error) {
		t.Fatal("request must not be sent")
		return nil, errors.New("unreachable")
	})})
	if _, err := client.Respond(ctx, model.Request{ProviderRequest: []byte(`not json`)}); err == nil {
		t.Fatal("expected setup error")
	}
	if events := modelCallEvents(t, &buf); len(events) != 0 {
		t.Fatalf("events = %+v, want none", events)
	}
}

func TestRespondRecordsTimingsFromRealTransport(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		frames := callTraceStreams["chat"]
		_, _ = io.WriteString(w, frames[0]+frames[1]+frames[2])
	}))
	defer server.Close()
	var buf bytes.Buffer
	ctx := log.WithLogger(t.Context(), slog.New(log.NewJSONHandler(&buf, nil)))
	client := adapterWireClients(wireClientConfig{baseURL: server.URL, httpClient: server.Client()})[0].client
	if _, err := client.Respond(ctx, model.Request{ProviderRequest: []byte(`{}`)}); err != nil {
		t.Fatalf("respond: %v", err)
	}
	events := modelCallEvents(t, &buf)
	if len(events) != 1 {
		t.Fatalf("events = %d", len(events))
	}
	for _, field := range []string{
		"model_call.headers_ms", "model_call.ttft_ms", "model_call.stream_complete_ms",
	} {
		if events[0][field] == nil {
			t.Errorf("%s is null", field)
		}
	}
}

func clientNamed(t *testing.T, name string, httpClient *http.Client) model.Client {
	t.Helper()
	for _, tc := range adapterWireClients(wireClientConfig{httpClient: httpClient}) {
		if tc.name == name {
			return tc.client
		}
	}
	t.Fatalf("no wire client %q", name)
	return nil
}
