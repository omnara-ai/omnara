# PostHog Analytics Agent

An agent that reads your PostHog for you. Each run it queries your project
through PostHog's hosted MCP server, compares yesterday against the trailing
7-day average, and delivers a short usage report:

> **412 active users (+9% vs 7-day avg), 18.2k events (+4%)**\
> **Top events:** pageview 9.1k (390 users), dashboard_viewed 2.4k (210), query_run 1.9k (140), invite_sent 310 (95), export_clicked 220 (60)\
> **Callouts:** invite_sent up 2.3x, almost all from one org; looks like a team onboarding, not a trend.

Reply to it wherever you use it (your own app, Slack, or the Omnara console)
to drill into any number ("why did signups spike?"). It's read-only against
PostHog.

## What you need

- An Omnara account ([app.omnara.com](https://app.omnara.com))
- A PostHog personal API key created with the
  [MCP Server preset](https://app.posthog.com/settings/user-api-keys?preset=mcp_server),
  which scopes it to one PostHog project. US and EU accounts both work, and
  MCP calls are free.

## Deploy

Open Claude Code, Codex, Cursor, or any other coding agent and say:

```text
Read https://raw.githubusercontent.com/omnara-ai/omnara/main/examples/posthog-analytics-agent/SKILL.md and follow it to deploy the PostHog analytics agent.
```

Your coding agent follows [SKILL.md](SKILL.md), which is plain steps with the
exact `npx omnara` commands if you'd rather run them yourself. Run it again
any time to change what it reports; it updates the agent in place.

## How it works

[agent.yaml](agent.yaml) is the whole agent, and it needs no machine pool:
Omnara calls PostHog's MCP server directly, sending the key as the bearer
token on each request, so the key never appears in the config or the event
log. The MCP URL limits the agent to PostHog's query tools and turns on
PostHog's read-only mode, so it can't change anything in your project. Each
run makes two standing queries plus at most a few follow-ups, and "steady
day" is a valid result. The metrics, the call budget, and the report format
are plain instruction text you can read and edit.
