# Custom tools over signed webhooks

A small TypeScript receiver runs a `text_length` custom tool and posts the
result back to Omnara. It verifies Standard Webhooks signatures before parsing
the payload and subscribes only to `tool_call_update`.

## Run

Requires Node 22+ and pnpm. From the repository root:

```sh
pnpm --dir frontend install --frozen-lockfile
cd examples/custom-tool-webhook
pnpm install
cp .env.example .env
```

This example uses the SDK from this checkout.

1. Generate a signing key with `openssl rand -base64 32`. Save it as a
   project-accessible **generic secret** in Omnara and keep the same value for
   the receiver.
2. Fill in `.env` with your personal access token, organization and project
   IDs, and the signing key from step 1.
3. Run `pnpm start`. Expose `http://127.0.0.1:3000` through an HTTPS tunnel.
   Set `event_webhook.url` in `agent.yaml` to the public URL ending in
   `/webhook` and `signing_secret_id` to the secret's `sec_…` ID. The config
   uses `omnara-openrouter` / `openai/gpt-5.6-sol`; choose another model if
   that one isn't available to your project.
4. In the Omnara dashboard, select the same organization and project, create
   an agent using `agent.yaml`, and send: **Count the characters in
   "hello 🌍".** The tool returns 7 Unicode code points.

`OMNARA_API_URL` optionally overrides `https://api.omnara.com/v1`; `PORT`
optionally overrides 3000.

## Delivery handling

The receiver verifies the original request bytes, signature, and timestamp,
then validates with `zEventWebhookPayload`. It queries the notified agent's
ready custom calls, following pagination to find the tool-call ID. Completed
or canceled calls are skipped. Other lifecycle states are acknowledged
without executing anything.

Concurrent notifications for the same tool call share one in-flight promise.
The receiver acknowledges only after result submission; API failures return
`500` so Omnara can retry. If another receiver already submitted a result or
the call stopped being ready, the API returns `409`, which is acknowledged.

The in-flight map is process-local, not durable deduplication. Restarts or
multiple receivers can repeat the calculation, which is safe because this
tool has no external side effects. For a tool that changes external state,
use its tool-call ID as an idempotency key in that system. Keep synchronous
work within Omnara's five-second delivery timeout; longer work needs a
durable job queue before acknowledging.

## Check

```sh
pnpm test
pnpm run typecheck
```
