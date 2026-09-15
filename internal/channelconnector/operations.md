# Bounded outgoing transport

This package provides the managed connector transport and typed send/read/interaction
contracts. Harness tools prepare scoped operations through executionstore, stream
authorized artifacts, and use the shared transactional completion mapper. The
gateway executes Slack operations. This client does not schedule or redispatch
operations; external customer tool waits have their own finite lifecycle.

`Config.OperationsURL string` uses `json:"operations_url,omitempty"`. It is the
complete deployment-owned endpoint, including its path. Normalization trims
outer whitespace and lowercases the host; it preserves path case and a deliberate
trailing slash. Validation permits absolute HTTP/HTTPS URLs (including private
HTTP services) and rejects userinfo, queries, fragments, encoded paths,
dot segments, doubled path separators, and invalid ports. Empty means inbound
authentication only. `Identity` never contains this endpoint or its token.

The public Go entry points are:

```go
func NewOperationsClient(configs []Config, client *http.Client) (*OperationsClient, error)
func (c *OperationsClient) Execute(ctx context.Context, request OperationRequest) (OperationResult, error)
func (e *OperationError) Error() string
func (e *OperationError) Unwrap() error
```

`NewOperationsClient` rejects duplicate connector IDs/tokens, duplicate outbound
capabilities within a connector, and multiple outbound endpoints for the same
exact `Capability{ConnectorKey, Provider}`. A connector without `operations_url`
does not supply an outbound route. Existing inbound capability deduplication is
unchanged. The client snapshots routes and disables redirects/cookies. A custom
HTTP transport must honor contexts and perform no mutation retries.

Request and result types are declared in [operations.go](operations.go):

```go
type OperationRequest struct {
    RequestID  string
    Capability Capability
    Kind       OperationKind
    Scope      OperationScope
    Payload    json.RawMessage
    Artifacts  []OperationArtifact
}
type OperationScope struct {
    ProjectID, IntegrationAppID, IntegrationInstallID, AgentID, ChannelID string
}
type OperationArtifact struct {
    ID, Filename, ContentType string
    Open func(ctx context.Context) (io.ReadCloser, error)
}
type OperationResult struct {
    RequestID string           // request_id
    Outcome   OperationOutcome // outcome
    Payload   json.RawMessage  // payload,omitempty
}
type OperationError struct {
    Outcome    OperationOutcome
    Code       string
    StatusCode int
    // Unwrap exposes only the caller's cancellation/deadline sentinel, if any.
}
```

`OperationKind` is `send`, `read`, or `interaction`, with exported constants
`OperationSend`, `OperationRead`, and `OperationInteraction`. `OperationOutcome`
is `completed`, `failed`, or `unknown`, with constants `OperationCompleted`,
`OperationFailed`, and `OperationUnknown`. Draft/publication and partial history
semantics belong in the completed operation's typed payload.

One POST carries `Authorization: Bearer <deployment connector token>`. The URL
comes exclusively from the selected route, never from request data. The caller
must supply a context deadline. The private JSON envelope contains:

```json
{
  "request_id": "stable-operation-id",
  "capability": {"connector_key": "chat_sdk", "provider": "slack"},
  "kind": "send",
  "scope": {
    "project_id": "resolved-project",
    "integration_app_id": "resolved-app",
    "integration_install_id": "resolved-install",
    "agent_id": "resolved-agent",
    "channel_id": "resolved-channel"
  },
  "deadline": "2026-09-14T12:00:00Z",
  "payload": {},
  "artifacts": [{"id": "authorized-artifact", "filename": "a.txt", "content_type": "text/plain"}]
}
```

Without artifacts, the body is `application/json` and `artifacts` is omitted.
With artifacts, the body is streamed `multipart/form-data`: the first part is
named `operation` with content type `application/json`; subsequent parts are
named `artifact`, in metadata order, with their filename and media type. Artifact
IDs are metadata, not part names or URLs. Sources open sequentially and close
before opening the next. `Open` must honor context cancellation; each reader's
`Close` must interrupt a concurrent blocked `Read`.

Limits are `MaxOperationArtifacts = 20`, `MaxMetadataBytes = 256 KiB` for operation payloads,
`MaxOperationEnvelopeBytes = 512 KiB`, and `MaxOperationResponseBytes = 1 MiB`.
There is **no universal product per-file egress ceiling** here. The existing
daemon upload tool has a 10 MiB ceiling (`internal/daemonprotocol/protocol.go`,
`internal/httpapi/daemon_artifact_routes.go`), but artifact storage accepts a
caller-specific `MaxBytes` (`internal/storage/artifactstore/artifact_preparation.go`),
and authorized stored artifacts stream through `artifactstore.OpenArtifactBlob`
without a new per-file size check.
The ingress ceiling must not silently become an outgoing-send restriction.

Go streams each authorized source until EOF under the single request deadline,
without buffering the file. The gateway requires positive finite deployment
`maxRequestBytes` (actual HTTP bytes including framing), `maxTemporaryBytes`
(actual disk bytes across requests), concurrency, memory, and duration budgets.
These operational budgets can be chosen to accommodate stored files; no default
200 MiB request maximum or 10 MiB file maximum is imposed by this contract.
The receiver must fully validate the entire finite multipart body before
provider mutation. An incomplete upload cannot be accepted merely because its
metadata arrived. Do not buffer all files in RAM.

HTTP 200 with `application/json` must contain a correlated terminal
`OperationResult`. Missing/malformed/oversized replies, HTTP errors (including
429/5xx), redirects, and disconnects after dispatch produce a non-nil
`OperationError`: unknown for mutations, failed for reads. Only the correlated
gateway envelope can establish a known mutation failure. Failed/unknown gateway
payloads are discarded; error codes are local fixed diagnostics, without raw
HTTP/source errors, response bodies, or token-bearing causes. No HTTP mutation
is retried by this client. No receipt, 202, or pending result is completion.

The new gateway helper exposes:

```ts
retryOperation<T>(
  options: OperationRetryOptions,
  operation: (context: OperationAttemptContext) => Promise<T>,
): Promise<T>
parseRetryAfter(value: string | null, nowMs?: number): number | undefined
new OperationRetryError(code: OperationFailureCode, outcomeUnknown: boolean, attempts: number)
```

`OperationRetryOptions` has `requestId: string`, `deadlineMs: number`, optional
`signal: AbortSignal`, and optional `idempotent: boolean` (default false).
`OperationAttemptContext` has `attempt: number` (1–4), `requestId: string`,
`deadlineMs: number`, and `signal: AbortSignal`. `OperationRetryError` exposes
`code`, `outcomeUnknown`, and `attempts`; it retains no raw provider cause.
Failure codes are `invalid_request`, `deadline_exceeded`, `canceled`,
`permanent_failure`, `retries_exhausted`, and `outcome_unknown`.

The helper makes one initial attempt and up to three retryable retries. Backoff
uses 125–250, 250–500, then 500–1000 ms, increased to honor `retryAfterMs` from
`ProviderDeliveryError`. A minimum delay that cannot fit fails immediately.
`parseRetryAfter` accepts seconds/HTTP dates; an excessively large valid seconds
value becomes Infinity so it stops retries instead of silently shortening the
delay. Unclassified failures are unknown and never retried. Unknown classified
failures can retry only with explicit whole-operation idempotence; all retries
keep the same identity/deadline. Prior uncertainty survives subsequent failures.

Integration remaining for the lead:

- Construct the client from deployment configuration and resolve live authority
  and exact connector capability before `Execute`; connect tool/interaction
  execution records and stable request identity.
- Supply `GatewayServerOptions.operations` to enable `/internal/operations`.
  The receiver has constant-time shared-token verification, envelope/scope
  validation, deadline/disconnect cancellation and deployment byte budgets.
  The executor must validate operation-specific payloads and live authority.
  See `plans/channel-integration/gateway-operations-checkpoint.md` for declared
  MIME parser semantics, cancellation regression coverage, and focused test results.
  This coding-worker validation is not independent reviewer clearance.
- Define typed send/read/interaction payload and completion/error mapping in the
  owning shared contract. Send data includes authorized content/params; read
  data includes bounded cursor/limit; interaction data references the canonical
  interaction. Validate publication and continuation before granting/exposing IDs.
- Feed actual provider I/O the attempt signal and stable provider idempotency
  key when supported. Disable SDK auto-retries, including retries in underlying
  upload/download clients, so hidden attempts do not exceed the helper's budget.
- Wire deployment examples and remove the outgoing outbox in the lead's separate
  scope. The real Go-to-Hono test in gateway operations.test.ts drives
  testdata/gateway-interop/main.go through send/read/interaction and a streamed
  12 MiB Unicode-named artifact. Concrete provider interoperability remains unwired.
