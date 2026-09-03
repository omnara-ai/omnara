# PostHog Pulse Agent

An agent that reads your PostHog for you. Every morning it queries your
project through the hosted
[PostHog MCP server](https://posthog.com/docs/model-context-protocol),
compares yesterday against the trailing 7-day average, and delivers a short
usage pulse — active users, event volume, top events, and callouts for
anything that moved more than ~30%. Reply in Slack (or the Omnara console)
to drill into any number ("why did signups spike?"). It is read-only
against PostHog.

The whole example is one notebook, [demo.ipynb](demo.ipynb). The agent
itself is a single config object — no service to deploy, and no machine
either: MCP calls are dispatched by Omnara's control plane, so the PostHog
key never leaves Omnara's server side. (The sibling
[x-signal-agent](../x-signal-agent) shows the other integration pattern —
an agent running `curl` on a pool machine with a secret injected as an
environment variable.)

## What you need

- An Omnara account ([app.omnara.com](https://app.omnara.com)) with an API key
- A PostHog personal API key created with the
  [MCP Server preset](https://app.posthog.com/settings/user-api-keys?preset=mcp_server)
  — MCP calls are free (rate-limited, not billed)
- [Deno](https://docs.deno.com/runtime/getting_started/installation/)

## Run it

```sh
brew install deno
deno jupyter --install    # register the Deno kernel with Jupyter

cd examples/posthog-pulse-agent
cp .env.example .env      # set OMNARA_API_KEY and POSTHOG_API_KEY
deno install              # fetch @omnara/sdk into node_modules
```

Deno is only needed to run the notebook (it provides the TypeScript Jupyter
kernel; `deno install` fetches `@omnara/sdk` from `package.json`). The SDK itself works
on Node and Bun too — you can copy the cells into a plain script if you
prefer.

Open `demo.ipynb` (VS Code, Cursor, or `jupyter lab`), pick the **Deno**
kernel, and run top to bottom. One cell holds the agent — the metrics, the
call budget, and the report format are plain-English instruction you can
edit. Every cell is idempotent: rerun after editing and the agent updates
in place (and the secret rotates to the current `.env` value).

The agent stays read-only by construction. Its MCP URL

```text
https://mcp.posthog.com/mcp?mode=tools&features=insights,data_schema,sql
```

filters PostHog's ~40+ tools down to the query surface (`query-*`,
`read-data-schema`, `execute-sql`) — write tools aren't enabled by default for this
agent. The key lives in an Omnara project secret referenced from the config's
`auth` block and is attached as the bearer header server-side; it never
appears in the config or event log. EU cloud accounts: swap the host for
`mcp-eu.posthog.com` in the agent cell. In the event stream, calls appear as
`mcp__posthog__query-trends` and so on.

## Slack and scheduling (optional)

The notebook can create a Slack app for you (set
`SLACK_APP_CONFIGURATION_TOKEN` in `.env`). Invite the bot to a channel and
mention it — the agent delivers the pulse in that thread, and thread replies
become instructions, so the team can drill into any number right there.
Without Slack, pulses appear in the console.

The last cell schedules a daily 9am pulse. Each firing launches a fresh
agent that reports on "yesterday" in your PostHog project's timezone and
recomputes the 7-day baseline from scratch — there is no state between
runs. Disable or delete the trigger in the console to stop.
