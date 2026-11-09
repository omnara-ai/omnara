# observability

`github.com/omnara-ai/omnara/observability` is the wide-event logging and
Prometheus infrastructure shared by Omnara services. It is a separate Go module
so other services can depend on it without importing the control plane.

- `wideevent` builds one structured log record per unit of work (an HTTP
  request, a job run), with attached database and outbound HTTP traces, level
  escalation, and secret scrubbing in the JSON handler.
- `metrics` provides the Prometheus registry and operator listener
  (`/metrics`, `/healthz`, `/readyz`) plus recorders for inbound HTTP,
  outbound HTTP and gRPC, pgx queries, and periodic jobs, and the control plane's
  daemon, memory, notification, and provider-runtime recorders.
  `metrics.WithNamespace` keeps each service's series distinct.
- `httprequest` completes each inbound HTTP request: it emits the request's
  wide event and its metrics observation from one final status, and recovers
  handler panics.

Service-specific event helpers stay in the consuming service. The
root module builds against this directory through a `replace` directive;
external consumers pin a version.

## Model call instrumentation

Every model provider attempt (Chat Completions, Responses, and Messages,
retries and failures included) emits one `model.call` wide event and one
`metrics.ModelCallRecorder` observation from `route.Client.RespondStream`.
Attempts that fail before a request is sent are not recorded.

Timings are milliseconds from request start; a phase the attempt never
reached is logged as `null` and not observed in metrics:

| Field | Phase |
| --- | --- |
| `upload_complete_ms` | transport finished writing the request body |
| `headers_ms` | response headers received |
| `ttft_ms` | time to first token: first streamed frame carrying model output |
| `stream_complete_ms` | stream reached its terminal event (success only) |
| `duration_ms` | attempt end, always set |

A token is a nonempty text, reasoning, or tool-argument delta, or the frame
that names a tool call (the model generates the name before the arguments);
whitespace counts. `first_token_kind` says which (`text`, `reasoning`,
`tool_call`). Role-only chunks, heartbeats, pings, text and thinking block
starts, and usage frames are metadata and never count. TTFT is `null` for a
non-streamed body, where every token arrives at once. When a provider hides
reasoning (encrypted or omitted thinking), the first observable token comes
after it, so TTFT includes reasoning time; `usage.reasoning_tokens` shows how
much. `http.subrequests.*.total_ms` on the parent event stops at response
headers, so it is not a TTFT measure.

The event also carries `request_bytes`, provider-reported input, cached,
cache-write, output, and reasoning tokens, `cache_ratio` (cached over input
tokens; `null` without usage), model, API format and variant, endpoint host,
outcome, error kind and code, and the call, model-call-context, and provider
request IDs. It never carries prompt or response content, headers, or
credentials.

Metrics (`omnara_model_call_*`) are labelled by `api_format`, `api_variant`,
`provider` (the endpoint host, which separates providers that share an API
variant), and `model`; IDs never become labels. `attempts_total` adds
`purpose`, `outcome`, and `error_kind`. The `ttft_seconds` and
`duration_seconds` histograms add nothing; their `_count` is the sample count.
Other phases are in logs only. The counters `request_bytes_total`,
`input_tokens_total`, `cached_input_tokens_total`,
`cache_write_input_tokens_total`, `output_tokens_total`,
`reasoning_tokens_total`, and `usage_missing_total` give averages when divided
by `attempts_total`, or for tokens by `attempts_total - usage_missing_total`.
The cache hit ratio is `cached_input_tokens_total / input_tokens_total`. TTFT
against exact request bytes or input tokens comes from `model.call` events,
which carry both per attempt.
`dashboards/model-calls.json` is a Grafana dashboard over these series.
