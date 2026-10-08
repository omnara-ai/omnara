---
name: posthog-analytics-agent
description: Deploy or update the PostHog Analytics Agent on Omnara, an agent that queries a PostHog project through PostHog's MCP server and delivers a daily usage report. Use when the user asks to deploy, set up, update, or remove the PostHog analytics agent.
---

# Deploy the PostHog Analytics Agent

You are setting up an Omnara agent for the user from `agent.yaml` in this
folder. The goal: an agent profile the user has watched run once, reachable
wherever they want it (their own app, Slack, Discord, or the Omnara
console), and
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
whatever the user picks in steps 7 and 8. Slack and Discord setup is simplest
with the CLI.

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

If `npx omnara integrations list --json` already has an integration named
`posthog-analytics-agent-slack` from an earlier run of this skill, reuse it
whatever its state, since names are unique in a project. If
`npx omnara integrations get <integration-id> --json` doesn't list this profile
under `settings.launcher.profiles`, add it with
`npx omnara integrations profiles <integration-id> --profile-ids <agent-profile-id>`;
the flag replaces the list, so repeat each profile already there. Then, if its
`state` is `active`, skip to its last step. If it's `disconnected`, setup didn't
finish: skip step 2, and redo step 1 only if the token in `.env` has expired.

1. The user creates an app configuration token at
   [api.slack.com/apps](https://api.slack.com/apps) under **Your App
   Configuration Tokens** → **Generate Token**. It expires after about 12
   hours. Have the user add it to `.env` as `SLACK_APP_CONFIG_TOKEN=...`.
2. Create the integration with this profile in its launcher, so a mention
   starts this agent, and note the `itg_…` ID it returns:

   ```sh
   npx omnara integrations create --name posthog-analytics-agent-slack \
     --integration-kind slack_thread \
     --settings '{"launcher":{"profiles":["<agent-profile-id>"]}}' --json
   ```

   The name is permanent and names the agent's Slack tools
   (`int__posthog-analytics-agent-slack__post_message`).
3. Create the Slack app and connect it:

   ```sh
   set -a && . ./.env && set +a
   npx omnara integrations slack <integration-id> --app-name "PostHog Analytics" \
     --app-configuration-token "$SLACK_APP_CONFIG_TOKEN"
   ```

   Ask before choosing a different app name; it's how the team mentions the
   bot. It opens a Slack authorization page that the user approves before it
   expires.
4. Tell the user to invite the bot to a channel (`/invite @PostHog Analytics`)
   and mention it: "@PostHog Analytics run today's report".

**Discord.** Works like Slack: the bot answers in a new thread wherever
it's mentioned in a server channel, and replies in the thread go to the
same agent. It doesn't answer direct messages.

If an integration named `posthog-analytics-agent-discord` already exists, reuse
it the same way: add this profile if it's missing. If it's `active`, skip to
step 5; Omnara can't tell whether steps 5 and 6 were done in Discord, so check
them with the user. If it's `disconnected`, skip step 3 and do the steps that
weren't done, passing the current `setup_revision` from
`npx omnara integrations get <integration-id> --json` in step 4.

1. The user creates an application named "PostHog Analytics" in the
   [Discord Developer Portal](https://discord.com/developers/applications);
   ask before choosing a different name, since it's how the team mentions
   the bot. They copy the **Application ID** and **Public Key** from
   **General Information**. Under **Bot**, they select **Reset Token** to copy
   the bot token, turn on **Message Content Intent**, and turn off **Public
   Bot** so only they can add it (if Discord refuses, first set
   **Installation** → **Install Link** to **None**). Have the user add the
   values to `.env`:

   ```sh
   DISCORD_APPLICATION_ID=...
   DISCORD_PUBLIC_KEY=...
   DISCORD_BOT_TOKEN=...
   ```

2. Store the bot token and note the `sec_…` ID. If a secret named
   `posthog-analytics-agent-discord-bot-token` already exists
   (`npx omnara secrets list --owner-kind project --owner-project-id <project-id> --name posthog-analytics-agent-discord-bot-token --json`),
   reuse its ID; if the user has reset the token since, have them paste the new
   one into it on the dashboard's **Secrets** page (the ID stays the same).
   Otherwise:

   ```sh
   set -a && . ./.env && set +a
   npx omnara secrets create --owner-kind project --owner-project-id <project-id> \
     --name posthog-analytics-agent-discord-bot-token --material-kind generic \
     --material-value "$DISCORD_BOT_TOKEN" --json
   ```

3. Create the integration with this profile in its launcher, and note the
   `itg_…` ID and `setup_revision` it returns:

   ```sh
   npx omnara integrations create --name posthog-analytics-agent-discord \
     --integration-kind discord_thread \
     --settings '{"launcher":{"profiles":["<agent-profile-id>"]}}' --json
   ```

   The name is permanent and names the agent's Discord tools
   (`int__posthog-analytics-agent-discord__post_message`).
4. Connect the bot. Omnara checks the token and finds the bot's user:

   ```sh
   set -a && . ./.env && set +a
   npx omnara integrations configure <integration-id> \
     --expected-setup-revision <setup-revision> \
     --provider-tenant-id "$DISCORD_APPLICATION_ID" \
     --credential-secret-id <secret-id> \
     --provider-config "{\"public_key\":\"$DISCORD_PUBLIC_KEY\"}" --json
   ```

5. Under **General Information**, the user sets **Interactions Endpoint URL**
   to `https://app.omnara.com/api/integrations/discord/<application-id>/interactions`
   and saves; the buttons on the bot's questions need it. Discord checks the
   URL when it's saved, so this has to come after step 4.
6. The user adds the bot to their server by opening this link, which asks
   only for the permissions the bot needs:
   `https://discord.com/oauth2/authorize?client_id=<application-id>&scope=bot&permissions=309237746752&integration_type=0`.
   Then they mention it in a channel: "@PostHog Analytics run today's report".

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
Slack or Discord channel, mention the bot in that channel once, then create
the trigger with `--target-type agent --target-agent-id <agent-id>` (the
agent ID from that conversation's console URL), so every report posts to that
thread.

## 9. Wrap up

Summarize what you created: the secret name, the profile, and the Slack or
Discord integration or cron trigger if any. Then tell the user:

- **Change what it reports or anything else:** ask a coding agent with this
  skill. It reuses the secret and updates the profile in place.
- **Remove it:** delete the cron trigger (`npx omnara crons delete <id>`),
  delete the Slack and Discord integrations, if any
  (`npx omnara integrations delete <integration-id>`), or, if other agents
  share it, rerun `npx omnara integrations profiles` with only their
  profiles. Then run `npx omnara profiles delete <agent-profile-id>` and
  `npx omnara secrets delete <secret-id>`. The PostHog key can also be
  revoked in PostHog's settings.
  With Discord, also delete the bot token secret
  (`npx omnara secrets delete <secret-id>`) and the application in the
  Discord Developer Portal.
