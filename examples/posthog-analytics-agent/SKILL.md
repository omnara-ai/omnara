---
name: posthog-analytics-agent
description: Deploy or update the PostHog Analytics Agent on Omnara, an agent that queries a PostHog project through PostHog's MCP server and delivers a daily usage report. Use when the user asks to deploy, set up, update, or remove the PostHog analytics agent.
---

# Deploy the PostHog Analytics Agent

You are setting up an Omnara agent for the user from `agent.yaml` in this
folder. The goal: an agent profile the user has watched run once, reachable
wherever they want it (their own app, Slack, or the Omnara console), and
optionally running on a daily schedule.

The steps below are the usual path and have the exact commands. Skip anything
that's already done (for example, the key and profile exist and the user only
wants to watch different events), follow the user's lead if they want a
different order, and narrate briefly as you go.

- Start from `agent.yaml` as written. If the user wants something different
  (the instruction, tools, model, or how they reach the agent), change it and
  tell them what you changed.
- If a command fails, read the error and fix it; `npx omnara <command> --help`
  and [docs.omnara.com](https://docs.omnara.com) have the details. If you're
  stuck, show the user the error.

The commands use the Omnara CLI (`npx omnara`) because its login handles auth
in one step; add `--json` when you need to read IDs from the output. They're a
reference, not a requirement: the Omnara MCP tools, the
[REST API](https://docs.omnara.com/api-reference/openapi.yaml), or the SDK work
too, and the flags map directly to API fields. What matters is the result:
a secret with the key, a profile from the filled-in `agent.yaml`, and
whatever the user picks in steps 7 and 8. Slack setup is simplest with the
CLI.

## 1. Connect to Omnara

1. Run `npx omnara whoami`. If it reports that you aren't logged in, run
   `npx omnara login` in an interactive terminal, share the approval link it
   prints, and wait until the user approves.
2. Pick the org and project. `npx omnara whoami --json` lists the user's orgs,
   and `npx omnara projects list --org <org-id> --json` lists an org's
   projects.
   - If there's only one org and one project, use them.
   - If there are several of either, ask the user which to use. Suggest the
     current defaults from `npx omnara config` if they're set, otherwise the
     project named `Default`.

   Save the choice with `npx omnara config --org <org-id> --project <project-id>`.

Remember the project ID; later steps need it.

## 2. Choose what to report (optional)

By default the report covers yesterday's active users, total event volume, top
events, and anything that moved more than about 30% against the 7-day
average. Ask whether there are specific events the user always wants to see,
such as signups, purchases, or a key feature. If so, add a line to the
instruction's numbered Fetch list in your copy of `agent.yaml` in step 5, for
example `3. Daily counts of user_signed_up and checkout_completed over the
same 8 days.`, and mention them in the Report section. Otherwise keep the
default.

## 3. Store the PostHog key

The agent queries PostHog through
[PostHog's hosted MCP server](https://posthog.com/docs/model-context-protocol),
which is free to call. It needs a PostHog personal API key created with the
**MCP Server** preset:
[app.posthog.com/settings/user-api-keys?preset=mcp_server](https://app.posthog.com/settings/user-api-keys?preset=mcp_server).
The preset scopes the key to one PostHog project, so the user picks the
project to report on there. US and EU accounts both work; PostHog routes the
key to the right region.

1. Check for an existing secret:
   `npx omnara secrets list --owner-kind project --owner-project-id <project-id> --name posthog-analytics-agent-api-key --json`.
   If one exists, ask whether to reuse it. To reuse it, note its `id` and skip
   to step 4. To change its value, have the user update it on the
   **Secrets** page of the Omnara dashboard (the ID stays the same) and skip to
   step 4.
2. Ask the user to add the key to a `.env` file in the current directory and
   tell you when it's saved (pasting it in the chat also works):

   ```sh
   POSTHOG_API_KEY=phx_...
   ```

3. Create the secret:

   ```sh
   set -a && . ./.env && set +a
   npx omnara secrets create --owner-kind project --owner-project-id <project-id> \
     --name posthog-analytics-agent-api-key --material-kind generic \
     --material-value "$POSTHOG_API_KEY" --json
   ```

   Note the `id` (`sec_…`) from the output.

## 4. Pick the model

`npx omnara grant models list --json`: each item has `model.provider_config`
and `model.name`. Ask the user which model to use. Suggest `openai/gpt-6.1-sol`
on `omnara-openrouter` if it's granted (it replaces GPT-6 Sol, which the
instruction was tested with); otherwise suggest a strong general-purpose model from the list. Name a couple
of alternatives rather than the whole list, and show everything if they ask.
The choice gives `MODEL_PROVIDER_CONFIG` and `MODEL_NAME`.

This agent needs no machine pool: PostHog's MCP server does all the querying.

## 5. Create the agent

1. Copy `agent.yaml` from this folder to `./posthog-analytics-agent.yaml` in the
   current directory. If this folder isn't available locally, download
   https://raw.githubusercontent.com/omnara-ai/omnara/main/examples/posthog-analytics-agent/agent.yaml
   instead. Add any events from step 2.
2. In the copy, replace every placeholder:

   | Placeholder | Value |
   | --- | --- |
   | `{{POSTHOG_KEY_SECRET_ID}}` | the `sec_…` ID from step 3 |
   | `{{MODEL_PROVIDER_CONFIG}}` | from step 4 |
   | `{{MODEL_NAME}}` | from step 4 |

   Then confirm nothing is left: `grep -n '{{' posthog-analytics-agent.yaml` must
   print nothing.
3. Check for an existing profile: `npx omnara profiles list --name posthog-analytics-agent --json`.
   - None: `npx omnara profiles create --name posthog-analytics-agent --file ./posthog-analytics-agent.yaml --json`
   - Exists: `npx omnara profiles update <agent-profile-id> --file ./posthog-analytics-agent.yaml --json`

   Note the profile's `id` and `current_config_id` from the output.

## 6. Get a first report

```sh
npx omnara agents launch --profile <agent-profile-id> --config <current-config-id> \
  --message "Run the daily PostHog usage report now." --json
```

Give the user the link to watch it work:
`https://app.omnara.com/projects/<project-id>/agents/<agent-id>`. The PostHog
calls show up as `mcp__posthog__…` tools, and a report takes about a minute.
The user can reply in the console to dig into any number, for example "why
did signups spike?".

## 7. Choose where to use it

Ask where the user wants to talk to the agent, offering these in this order.
Any combination is fine.

**Their own app, existing or new (recommended).** Put the agent where the
user's team already works: inside their existing product or internal tool, or, if there's no
natural home, a small UI built for it. Both are the same integration: launch
agents from the profile and stream their replies with the TypeScript SDK
(`@omnara/sdk`) or the REST API; see [docs.omnara.com](https://docs.omnara.com).
Anything else that can call the API (an alerting webhook, a CI job, a script)
can start the agent the same way. The app needs an org API key: the user
creates one on the dashboard's **API Tokens** page (**Organization** tab →
**New token**), then grants it a role on this project from the key's detail
panel. Help them wire it in, or build the UI with them.

**Slack.** The bot answers wherever it's mentioned, and thread replies become
instructions to the agent, so the team can drill into any number in the
thread.

1. Open the project's **Integrations** page. Create a **Slack bot** integration
   with a descriptive name, or reuse the integration already created for this
   example. In its launch settings, select this agent profile. Preserve other
   selected profiles and settings when updating an existing integration.
2. Connect the Slack app from that page, using an app configuration token from
   [Slack's app settings](https://api.slack.com/apps), and complete OAuth. For CLI
   setup of that saved integration, put `SLACK_APP_CONFIG_TOKEN=...` in `.env` and run:

   ```sh
   set -a && . ./.env && set +a
   npx omnara integrations slack <integration-id> --app-name "PostHog Analytics" \
     --app-configuration-token "$SLACK_APP_CONFIG_TOKEN"
   ```

   Use the integration's `itg_…` ID, not the profile ID. The user approves the
   returned OAuth URL before it expires. Setup reconnects that integration;
   selecting the profile in its launcher enables new Slack conversations.
3. Tell the user to invite the bot to a channel (`/invite @PostHog Analytics`)
   and mention it: "@PostHog Analytics run today's report".

**Omnara console.** Already done: every agent launched from the profile shows
up in the console.

## 8. Run it daily (optional)

A schedule works with any of the above. Ask whether the user wants a daily
report, and for the time and timezone (default: every day at 9am in the user's
timezone). Set `--cron` and `--timezone` from their answer:

```sh
npx omnara crons create --name posthog-analytics-agent-daily \
  --target-type profile --target-agent-profile-id <agent-profile-id> \
  --cron '0 9 * * *' --timezone America/Los_Angeles \
  --message-template 'Run the daily PostHog usage report.' --json
```

If `npx omnara crons list --name posthog-analytics-agent-daily --json` already
shows a trigger, change it with `npx omnara crons update <cron-trigger-id>`
(same flags) instead of creating a second one.

Each firing launches a fresh agent from the profile. It reports on
"yesterday" in the PostHog project's timezone and recomputes the 7-day
baseline, so there is no state between runs. It shows up in the console, and
the user's app can pick it up through the SDK or API. To get each report in a
Slack channel, mention the bot in that channel once, then create the trigger
with `--target-type agent --target-agent-id <agent-id>` (the agent ID from
that conversation's console URL), so every report posts to that thread.

## 9. Wrap up

Summarize what you created: the secret name, the profile, and the Slack app or
cron trigger if any. Then tell the user:

- **Change what it reports or anything else:** ask a coding agent with this
  skill. It reuses the secret and updates the profile in place.
- **Remove it:** delete the cron trigger (`npx omnara crons delete <id>`),
  remove this profile from the integration's launcher on the project's Integrations page, then
  `npx omnara profiles delete <agent-profile-id>` and
  `npx omnara secrets delete <secret-id>`. The PostHog key can also be
  revoked in PostHog's settings.
