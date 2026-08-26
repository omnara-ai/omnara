# posthog-pulse-agent example

A daily product-metrics agent built on Omnara: every morning it queries your
PostHog project through the hosted
[PostHog MCP server](https://posthog.com/docs/model-context-protocol),
compares yesterday against the trailing 7-day average, and delivers a short
usage pulse — active users, event volume, top events, and callouts for
anything that moved more than ~30%. The agent stays available after each
pulse: reply in the Slack thread (or the console) with follow-up questions
like "why did signups spike?" and it answers with fresh queries. It is
read-only against PostHog.

The whole example is [demo.ipynb](demo.ipynb), a Jupyter notebook on the
[Deno kernel](https://docs.deno.com/runtime/reference/cli/jupyter/) using the
Omnara TypeScript SDK (`@omnara/sdk`). The agent itself is one config object
in one cell — an instruction, a model, one MCP server, and two delivery
tools. There is no service to deploy and **no machine either**: MCP tool
calls are dispatched by Omnara's control plane, so nothing runs on a pool
machine and the PostHog key never leaves Omnara's server side. (Contrast
with the sibling [x-signal-agent](../x-signal-agent) example, which shows
the other integration pattern: an agent running `curl` on a pool machine
with a secret injected as an environment variable.)

Cell by cell the notebook: resolves your org and project; upserts the
PostHog API key as a project secret; uploads the config as an agent
profile; launches a pulse and follows its live event stream; and
(optionally) connects Slack and creates a daily
[cron trigger](https://docs.omnara.com/api-reference/endpoints/cron-triggers/create-cron-trigger).
The same config can also be expressed as YAML and pasted into the console —
JSON and YAML are two `source_format`s of the same agent config.

The notebook runs against your default org and project — new Omnara accounts
come with both. The only console prerequisite is the profile's model being
granted to the project (new accounts get that from onboarding too).

## PostHog access

One value in `.env`: `POSTHOG_API_KEY`, a personal API key created with the
[MCP Server preset](https://app.posthog.com/settings/user-api-keys?preset=mcp_server),
which scopes it to one PostHog project. The agent config points at
PostHog's hosted MCP endpoint with that key as bearer auth:

```text
https://mcp.posthog.com/mcp?mode=tools&features=insights,data_schema,sql
```

The URL parameters do real work:

- `features=insights,data_schema,sql` filters the server's ~40+ tools down
  to the read-only query surface — the `query-*` wrappers (trends, funnels,
  retention…), `read-data-schema`, and `execute-sql`. Creation, update, and
  deletion tools (dashboards, insights, feature flags, surveys…) are not
  exposed to the agent at all.
- `mode=tools` pins one MCP tool per PostHog tool; without it most clients
  get a single CLI-style wrapper tool.

EU cloud accounts should swap the host for `mcp-eu.posthog.com` in the
notebook's agent cell. Calls to the MCP server are free (rate-limited, not
billed); the instruction caps each run at 8 calls anyway.

## Setup

```sh
brew install deno         # or https://docs.deno.com/runtime/getting_started/installation/
deno jupyter --install    # register the Deno kernel with Jupyter

cd examples/posthog-pulse-agent
cp .env.example .env      # set OMNARA_API_KEY and POSTHOG_API_KEY
```

There is nothing to `npm install`: [deno.json](deno.json) maps `@omnara/sdk`
to the SDK source in this repo (swap to `npm:@omnara/sdk` once published),
and Deno fetches its `zod` dependency itself.

Edit the `model` block in the notebook's agent cell to name a model provider
config and configured model from your organization — copy the exact names
from the console's **Models** page. Then open `demo.ipynb` (VS Code, Cursor,
or `jupyter lab`), pick the **Deno** kernel, and run the cells top to bottom.

Every cell is idempotent: rerun the notebook after editing the agent (for
example to add a metric, change the anomaly threshold, or track a specific
event) and it updates the profile in place. Rerunning also rotates the
secret to the current `POSTHOG_API_KEY` value.

## Slack

The pulse is most useful in a channel. Set `SLACK_APP_CONFIGURATION_TOKEN`
in `.env` to an app configuration token from
[api.slack.com/apps](https://api.slack.com/apps) and run the notebook's
Slack cell — or use **Deploy to app → Slack** on the profile's row in the
console; the [Slack integration](https://docs.omnara.com/integrations/slack)
guide covers the flow. Once installed, cron-triggered runs deliver the pulse
with `send_integration_message`. Thread replies become agent inputs, so the
team can drill into any number ("break active users down by country") right
in the thread. Without Slack, pulses appear in the console (and in the
notebook when you launch from there).

## Schedule

The notebook's last cell creates a cron trigger named
`posthog-pulse-agent-daily` (`0 9 * * *` America/Los_Angeles — edit the cell
to change it). Each firing launches a fresh agent from the profile. Every
run reports on "yesterday" in your PostHog project's timezone and recomputes
the 7-day baseline from scratch, so there is no state to store between runs.
Disable or delete the trigger in the console to stop scheduled pulses.

## Notes

- The agent is read-only by construction three times over: the MCP URL's
  feature filter exposes only query tools, the API key can be limited to
  read scopes, and the instruction forbids mutations. The interesting one
  is the first — the write tools don't exist as far as this agent is
  concerned.
- The PostHog key lives in an Omnara project secret referenced from the MCP
  server's `auth` block (`type: bearer, secret_id: sec_…`). Omnara attaches
  it as the `Authorization` header on every MCP call server-side; it never
  appears in the agent config, the event log, or on any machine.
- The instruction fixes two standing queries (the 8-day daily trend and
  yesterday's top events) and allows at most 3 more per run to chase
  anomalies. The budget is deliberate: an unbounded "here's everything in
  your analytics" report trains the team to ignore it. "Steady day" is a
  valid pulse.
- In the event stream, PostHog calls appear as `mcp__posthog__query-trends`,
  `mcp__posthog__execute-sql`, and so on — the `mcp__<server>__<tool>`
  namespace is how Omnara exposes remote MCP tools to the model.
