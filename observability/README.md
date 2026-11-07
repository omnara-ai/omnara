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
| `first_content_ms` | first nonempty text, reasoning, or tool-argument delta (TTFT) |
| `first_text_ms` | first non-whitespace visible text |
| `stream_complete_ms` | stream reached its terminal event (success only) |
| `duration_ms` | attempt end, always set |

Heartbeats, pings, block starts, and other metadata frames never count as
content. `http.subrequests.*.total_ms` on the parent event stops at response
headers, so it is not a TTFT measure.

The event also carries `request_bytes`, provider-reported input, cached,
cache-write, output, and reasoning tokens, `cache` (`hit`, `miss`, `unknown`),
model, API format and variant, endpoint host, outcome, error kind and code, and
the call, model-call-context, and provider request IDs. It never carries
prompt or response content, headers, or credentials.

Metrics (`omnara_model_call_*`) are labelled by `api_format`, `api_variant`,
and `model`; IDs never become labels. TTFT histograms add `cache` and a
zero-padded size bucket (`input_tokens_le`, `request_bytes_le`) so TTFT can be
graphed against size; `_count` gives the sample count for every cell.
`dashboards/model-calls.json` is a Grafana dashboard over these series.
