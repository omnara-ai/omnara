# observability

`github.com/omnara-ai/omnara/observability` is the wide-event logging and
Prometheus infrastructure shared by Omnara services. It is a separate Go module
so other services can depend on it without importing the control plane.

- `wideevent` builds one structured log record per unit of work (an HTTP
  request, a job run), with attached database and outbound HTTP traces, level
  escalation, and secret scrubbing in the JSON handler.
- `metrics` provides the Prometheus registry and operator listener
  (`/metrics`, `/healthz`, `/readyz`) plus recorders for inbound HTTP,
  outbound HTTP, pgx queries, and periodic jobs, and the control plane's
  daemon, memory, notification, and provider-runtime recorders.
  `metrics.WithNamespace` keeps each service's series distinct.
- `httprequest` completes each inbound HTTP request: it emits the request's
  wide event and its metrics observation from one final status, and recovers
  handler panics.

Service-specific event helpers stay in the consuming service. The
root module builds against this directory through a `replace` directive;
external consumers pin a version.
