# @omnara/sdk

TypeScript SDK for the [Omnara](https://omnara.com) API. Omnara is an open-source managed agent platform for running AI agents on any model and any machine.

## Installation

```sh
npm install @omnara/sdk
```

## Usage

```ts
import { createOmnaraClient, bearerToken } from '@omnara/sdk'

const client = createOmnaraClient({
  baseUrl: 'https://api.omnara.com/v1',
  auth: bearerToken('your-api-key'),
})
```

### Entry points

- `@omnara/sdk` — Node client, auth, and event streaming
- `@omnara/sdk/browser` — browser-safe client
- `@omnara/sdk/tanstack` — TanStack Query hooks (requires the optional `@tanstack/react-query` peer dependency)
- `@omnara/sdk/zod` — Zod schemas for all API types

Requires Node.js 22 or later.

### Retries and deadlines

Clients created with `createOmnaraClient` retry temporary network failures and HTTP
408, 429, 500, 502, 503, and 504 responses up to twice. The SDK only retries reads
and writes whose API contract guarantees replay safety. Other writes get one
attempt. It uses exponential backoff with jitter and honors `Retry-After`; a
server wait longer than 60 seconds returns the error so your application can
schedule a later attempt.

```ts
import { bearerToken, createOmnaraClient, sdk } from '@omnara/sdk'

const client = createOmnaraClient({
  auth: bearerToken('your-api-key'),
  maxRetries: 2, // Retries after the first attempt; 0 disables them.
  timeoutMs: 30_000, // Total budget for one SDK call; 0 (the default) means no deadline.
})

await sdk.createAgentInput({
  client,
  path: { orgID, projectID, agentID },
  body: { content_blocks: [{ type: 'text', text: 'Hello' }] },
  headers: { 'Idempotency-Key': durableJobID },
  maxRetries: 0, // Both settings can be overridden for one call.
  timeoutMs: 5_000,
  signal: controller.signal,
})
```

For endpoints accepting `Idempotency-Key`, the SDK generates a key when none was
supplied. Every attempt within that call uses the same key, body, and authenticated
request. Separate SDK calls get separate keys. Supply your own stable key when
repeating a job across calls or process restarts. A key cannot make an endpoint
replay-safe unless that endpoint supports it. The SDK rejects `Idempotency-Key`
in client-wide headers: each logical operation needs its own key.

Replay safety protects an operation's effect, not its response. For example,
`submitToolCallResult` can return `409 tool_call_already_completed` after a lost
response to a successful submission. This means the call is complete; it does
not prove which submission supplied the stored result.

`timeoutMs` includes authentication, response parsing, and retry waits. If the
next wait cannot fit, the SDK returns the last API error immediately. Expiring
during a request raises a `TimeoutError`; caller cancellation preserves
`signal.reason`. Both settings must be non-negative integers; `timeoutMs` must
fit a 32-bit timer. The existing `throwOnError: false` response modes still apply.

Authentication, serialization, and interceptors run once per logical call, not
once per attempt. If a validator changes the method, route template, or base URL,
or an interceptor changes the constructed request's method or URL, the SDK makes
one attempt because the original replay contract no longer applies.
Clearing the client's request interceptors also disables retries.
Header changes and clones that preserve the destination remain retryable.
A custom `fetch` still sees each attempt. Retries stop at the
fetch boundary: a failure while reading or parsing a JSON response body is
returned without another attempt. Streamed request bodies and `parseAs: 'stream'`
requests are not retried. For a successfully returned stream, including automatic
parsing, the request deadline does not limit subsequent body reads; use `signal`
to cancel them. HTTP error-body parsing remains covered by the deadline.
The SDK releases its caller cancellation listener when the request settles, or
when a returned stream ends, errors, or is canceled. `openAgentEventStream` owns
its reconnection policy and releases that listener after each connection.
The lower-level generated `sdk.streamEvents` uses its existing `sse*` options
instead of `maxRetries` and `timeoutMs`.

Pass your configured `client` to generated functions and TanStack options. Calls
without one use a shared default client with `maxRetries: 0` and `timeoutMs: 0`.
Explicit per-call retry and deadline options still work, including through the
TanStack entry point. This client keeps the generated client's authentication and
raw error behavior; it does not add `ApiError` mapping. When enabling SDK retries
with TanStack Query, set `retry: false` to avoid multiplying attempts; query
refetches on reconnect remain independent of transport retries.

### Request observations

An optional `onRequestComplete` callback receives one local summary per SDK call,
including its total duration, fetch attempts, time spent waiting between attempts,
final HTTP status when available, and promise settlement. It can be set on the
client or overridden per call:

```ts
import { bearerToken, createOmnaraClient } from '@omnara/sdk'

const client = createOmnaraClient({
  auth: bearerToken('your-api-key'),
  onRequestComplete(observation) {
    localMetrics.record(observation)
  },
})
```

`outcome` is `returned`, `threw`, `canceled`, or `timeout`. A nonthrowing HTTP
failure has outcome `returned`, so inspect `status` as well. `operation` contains
a known API method and route template, or `unknown`; summaries never
contain resolved URLs, identifiers, credentials, or request/response payloads.
For raw streams, the summary ends when the SDK returns the stream; subsequent
body consumption is outside the request observation.

Keep callbacks cheap: they run synchronously at settlement, their promises are
not awaited, and thrown errors or rejected promises cannot affect the request.
The SDK does not export observations over the network or install telemetry.

## Documentation

The [quickstart](https://docs.omnara.com/quickstart) shows how to launch an agent with the SDK. See also the [Omnara docs](https://docs.omnara.com) and the [GitHub repository](https://github.com/omnara-ai/omnara).

## License

Apache-2.0
