# @omnara/sdk

TypeScript SDK for the [Omnara](https://omnara.com) API.

## Installation

```sh
npm install @omnara/sdk
```

## Usage

```ts
import { createOmnaraClient, bearerToken } from '@omnara/sdk'

const client = createOmnaraClient({
  baseUrl: 'https://api.omnara.com',
  auth: bearerToken('your-api-key'),
})
```

### Entry points

- `@omnara/sdk` — Node client, auth, and event streaming
- `@omnara/sdk/browser` — browser-safe client
- `@omnara/sdk/tanstack` — TanStack Query hooks (requires the optional `@tanstack/react-query` peer dependency)
- `@omnara/sdk/zod` — Zod schemas for all API types

Requires Node.js 22 or later.

### Follow an agent event stream

`openAgentEventStream` reconnects transient failures and resumes after the last durable event it delivered. Give the follower an abort signal so the owner of the operation also owns its lifetime:

```ts
import { openAgentEventStream } from '@omnara/sdk'

const controller = new AbortController()
// Call controller.abort() from the owning task or UI cleanup path.

for await (const frame of openAgentEventStream({
  client,
  path: { orgID, projectID, agentID },
  query: { stream_deltas: true },
  signal: controller.signal,
  onConnectionStateChange(state) {
    if (state.state === 'reconnecting') {
      clearBestEffortPreviews()
    } else if (state.reconnected) {
      refreshOpenInteractions()
    }
  },
})) {
  handleAgentFrame(frame)
}
```

Caller cancellation and breaking out of the loop complete normally. If a `next()` read is already pending, use the abort signal for prompt cancellation; an iterator `return()` waits behind that read. The follower retries HTTP 408, 429, and 5xx responses; fetch or reader disconnects; unexpected end-of-response; active-read timeout; and in-band `service_unavailable` errors. Authentication, authorization, invalid requests, API contract violations, and other terminal failures are thrown as `AgentEventStreamError`. Deltas are best-effort; use durable events as the source of truth after a reconnect.

#### Migrating the event stream follower

Earlier prerelease SDKs returned `Promise<{ stream }>` and required applications to retry around that one physical connection. `openAgentEventStream` now returns the continuous async generator directly and owns transient retries. Remove `await`/`{ stream }` destructuring and any surrounding retry loop. Caller abort now completes normally; terminal failures use `AgentEventStreamError` kinds instead of public `aborted` and `retryable` flags.

## Documentation

See the [Omnara docs](https://docs.omnara.com) and the [GitHub repository](https://github.com/omnara-ai/omnara).

## License

Apache-2.0
