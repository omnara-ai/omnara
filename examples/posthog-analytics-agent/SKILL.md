---
name: posthog-analytics-agent
description: Deploy or update the PostHog Analytics Agent on Omnara, an agent that queries a PostHog project through PostHog's MCP server and delivers a daily usage report. Use when the user asks to deploy, set up, update, or remove the PostHog analytics agent.
---

# Deploy the PostHog Analytics Agent

You are setting up an Omnara agent for the user from `agent.yaml` in this
folder. The goal: an agent profile the user has watched run once, reachable
wherever the user wants it, and optionally running on a daily schedule.

The steps below are the usual path, with the exact commands. Skip anything
that's already done (for example, the key and profile exist and the user only
wants to watch different events), and follow the user's lead if they want a
different order.

- Start from `agent.yaml` as written. If the user wants something different
  (the instruction, tools, model, or how they reach the agent), change it and
  tell them what you changed.
- If a command fails, `npx omnara <command> --help` and
  [docs.omnara.com](https://docs.omnara.com) have the details.
- Stop only where a step says to ask the user. Ask in a sentence or two of
  plain language, together with the next step's question if one comes right
  after, then end your turn and wait for the reply.
- Where a step gives a default, use it without asking and say in a line what
  you picked, so the user can change it.
- When a step needs a token, the user can paste it in the chat or add it to a
  `.env` file in the current directory, whichever they prefer; offer both. If
  they paste it, write it to `.env` yourself so the commands work as written.

The commands use the Omnara CLI (`npx omnara`); add `--json` to read IDs.
They're a reference, not a requirement: the Omnara MCP tools, the
[REST API](https://docs.omnara.com/api-reference/openapi.yaml), and the SDK work
too, and the flags map directly to API fields.

## 1. Connect to Omnara

Run `npx omnara whoami`. If you aren't logged in, run `npx omnara login` without
piping its output, and keep it running: it waits until the user approves. It
opens the approval page in the user's browser and prints the page's link and a
code. Show the user both right away, since you may be on a different machine
than their browser, and ask them to approve once the code matches. If the
request expires, run it again. If login says the account has no organization
yet, have the user create one at the link it prints. Then `npx omnara whoami`
should succeed.

Pick the org and project (`npx omnara whoami --json` lists orgs,
`npx omnara projects list --org <org-id> --json` their projects): use them if
there's one of each, otherwise the defaults from `npx omnara config` or the
project named `Default`; ask only if there's still no clear choice. Save the
choice with `npx omnara config --org <org-id> --project <project-id>`; later
steps need the project ID.

## 2. Choose what to report (optional)

By default the report covers yesterday's active users, total event volume, top
events, and anything that moved more than about 30% against the 7-day
average. If the user has named events they always want to see, such as
signups, purchases, or a key feature, add a line to the instruction's numbered
Fetch list in your copy of `agent.yaml` in step 5, for example `3. Daily counts
of user_signed_up and checkout_completed over the same 8 days.`, and mention
them in the Report section. Otherwise keep the default; they can ask for more
after the first report.

## 3. Store the PostHog key

The agent queries PostHog through
[PostHog's hosted MCP server](https://posthog.com/docs/model-context-protocol),
which is free to call. It needs a PostHog personal API key created with the
**MCP Server** preset:
[app.posthog.com/settings/user-api-keys?preset=mcp_server](https://app.posthog.com/settings/user-api-keys?preset=mcp_server).
The preset scopes the key to one PostHog project, so the user picks the
project to report on there. US and EU accounts both work; PostHog routes the
key to the right region.

If a secret named `posthog-analytics-agent-api-key` already exists
(`npx omnara secrets list --owner-kind project --owner-project-id <project-id> --name posthog-analytics-agent-api-key --json`),
reuse it and note its `id`; if the value changed, the user updates it on the
dashboard's **Secrets** page (the ID stays the same). Otherwise get
`POSTHOG_API_KEY` (it starts with `phx_`) from the user, then create the secret
and note its `id` (`sec_…`):

```sh
set -a && . ./.env && set +a
npx omnara secrets create --owner-kind project --owner-project-id <project-id> \
  --name posthog-analytics-agent-api-key --material-kind generic \
  --material-value "$POSTHOG_API_KEY" --json
```

## 4. Pick the model

`npx omnara grant models list --json`: each item has `model.provider_config` and
`model.name`. Use `openai/gpt-6.1-sol` on `omnara-openrouter` if it's granted,
otherwise a strong general-purpose model from the list. The choice gives
`MODEL_PROVIDER_CONFIG` and `MODEL_NAME`.

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

## 7. Choose where and when to use it

Right after sharing the link, ask in one message where the user wants to talk to
the agent and whether it should send a daily report (suggest every day at 9am in
their timezone). Wait for the reply; don't assume the console or a schedule. Any
combination works:

- **Their own app (recommended):** inside their product or internal tool, or a
  small UI built for it.
- **Slack or Discord:** the team mentions the bot, and replies in the thread go
  to the same agent, so follow-ups can drill into any number.
- **Only the Omnara console:** nothing to set up; every agent launched from the
  profile shows up there.

Set up whichever the user picks with [`integrations.md`](../integrations.md)
(read it from
https://raw.githubusercontent.com/omnara-ai/omnara/main/examples/integrations.md
if you don't have the repo). For this agent, name the integrations
`posthog-analytics-agent-slack` and `posthog-analytics-agent-discord`, call the
bot "PostHog Analytics", and have the user try it with "@PostHog Analytics run
today's report".

If they want a daily report, create it with `--cron` and `--timezone` from their
answer:

```sh
npx omnara crons create --name posthog-analytics-agent-daily \
  --target-type profile --target-agent-profile-id <agent-profile-id> \
  --cron '0 9 * * *' --timezone America/Los_Angeles \
  --message-template 'Run the daily PostHog usage report.' --json
```

If `npx omnara crons list --name posthog-analytics-agent-daily --json` already
shows a trigger, change it with `npx omnara crons update <cron-trigger-id>`
(same flags) instead of creating a second one.

Each firing launches a fresh agent from the profile. It reports on "yesterday"
in the PostHog project's timezone and recomputes the 7-day baseline, so there is
no state between runs. It shows up in the console, and the user's app can pick
it up through the SDK or API. To post every report in one Slack or Discord
thread instead, see "Scheduled runs in a channel" in `integrations.md`.

## 8. Wrap up

Summarize what you created: the secret, the profile, and any integrations or
cron trigger. Remind the user to delete `.env` or move what's in it somewhere
safe. Then tell them:

- **Change what it reports or anything else:** ask a coding agent with this
  skill. It reuses the secret and updates the profile in place.
- **Remove it:** delete the cron trigger (`npx omnara crons delete <id>`),
  remove its integrations, if any (see "Removing" in `integrations.md`),
  then run `npx omnara profiles delete <agent-profile-id>` and
  `npx omnara secrets delete <secret-id>`. The PostHog key can also be
  revoked in PostHog's settings.
