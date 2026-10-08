---
name: x-signal-agent
description: Deploy or update the X Signal Agent on Omnara, an agent that searches X for posts about a topic and delivers a daily digest. Use when the user asks to deploy, set up, update, or remove the X signal agent.
---

# Deploy the X Signal Agent

You are setting up an Omnara agent for the user from `agent.yaml` in this
folder. The goal: an agent profile the user has watched run once, reachable
wherever the user wants it, and optionally running on a daily schedule.

The steps below are the usual path, with the exact commands. Skip anything
that's already done (for example, the token and profile exist and the user only
wants a new topic), and follow the user's lead if they want a different order.

- Start from `agent.yaml` as written. If the user wants something different
  (the instruction, tools, model, or how they reach the agent), change it and
  tell them what you changed.
- If a command fails, `npx omnara <command> --help` and
  [docs.omnara.com](https://docs.omnara.com) have the details.

The commands use the Omnara CLI (`npx omnara`); add `--json` to read IDs.
They're a reference, not a requirement: the Omnara MCP tools, the
[REST API](https://docs.omnara.com/api-reference/openapi.yaml), and the SDK work
too, and the flags map directly to API fields.

## 1. Connect to Omnara

Run `npx omnara whoami`. If you aren't logged in, run `npx omnara login` in an
interactive terminal, share the approval link it prints, and wait for the user
to approve. Then pick the org and project (`npx omnara whoami --json` lists
orgs, `npx omnara projects list --org <org-id> --json` their projects): use them
if there's one of each, otherwise ask, suggesting the defaults from
`npx omnara config` or the project named `Default`. Save the choice with
`npx omnara config --org <org-id> --project <project-id>`; later steps need the
project ID.

## 2. Choose what to track

Ask the user what they want to hear about on X: their product category,
competitors, customers' pain points, or any topic. Then:

- Write `TOPIC`: a short phrase the agent judges relevance against, for
  example `managed-agent infrastructure`.
- Write `X_SEARCH_QUERY` with
  [X search operators](https://docs.x.com/x-api/posts/search/integrate/build-a-query),
  for example
  `("managed agents" OR "agent infrastructure") -is:retweet lang:en`.
  Keep it under 512 characters, end it with `-is:retweet lang:en` unless the
  user wants retweets or other languages, and do not use single quotes.

Show both to the user and adjust until they're happy.

## 3. Store the X bearer token

The agent needs an X API bearer token with pay-per-use billing. The user gets
it at [console.x.com](https://console.x.com) under their app's **Keys and
tokens**. A daily scan costs on the order of cents.

If a secret named `x-signal-agent-bearer-token` already exists
(`npx omnara secrets list --owner-kind project --owner-project-id <project-id> --name x-signal-agent-bearer-token --json`),
ask whether to reuse it and note its `id`; if the value changed, the user
updates it on the dashboard's **Secrets** page (the ID stays the same).
Otherwise have the user add `X_BEARER_TOKEN=...` to a `.env` file in the current
directory (pasting it in the chat also works), then create the secret and note
its `id` (`sec_…`):

```sh
set -a && . ./.env && set +a
npx omnara secrets create --owner-kind project --owner-project-id <project-id> \
  --name x-signal-agent-bearer-token --material-kind generic \
  --material-value "$X_BEARER_TOKEN" --json
```

## 4. Pick the machine pool and model

1. `npx omnara grant pools list --json`: each grant's `machine_pool.name` is a
   candidate `MACHINE_POOL`; the agent runs `curl` on a machine from this
   pool. Use the only one, or ask if there are several. If there are none,
   stop and tell the user the project needs a machine pool.
2. `npx omnara grant models list --json`: each item has `model.provider_config`
   and `model.name`. Ask the user which model to use, suggesting
   `openai/gpt-6.1-sol` on `omnara-openrouter` if it's granted, otherwise a
   strong general-purpose model from the list. The choice gives
   `MODEL_PROVIDER_CONFIG` and `MODEL_NAME`.

## 5. Create the agent

1. Copy `agent.yaml` from this folder to `./x-signal-agent.yaml` in the
   current directory. If this folder isn't available locally, download
   https://raw.githubusercontent.com/omnara-ai/omnara/main/examples/x-signal-agent/agent.yaml
   instead.
2. In the copy, replace every placeholder:

   | Placeholder | Value |
   | --- | --- |
   | `{{TOPIC}}` | from step 2 |
   | `{{X_SEARCH_QUERY}}` | from step 2 |
   | `{{X_TOKEN_SECRET_ID}}` | the `sec_…` ID from step 3 |
   | `{{MACHINE_POOL}}` | from step 4 |
   | `{{MODEL_PROVIDER_CONFIG}}` | from step 4 |
   | `{{MODEL_NAME}}` | from step 4 |

   Then confirm nothing is left: `grep -n '{{' x-signal-agent.yaml` must print
   nothing.
3. Check for an existing profile: `npx omnara profiles list --name x-signal-agent --json`.
   - None: `npx omnara profiles create --name x-signal-agent --file ./x-signal-agent.yaml --json`
   - Exists: `npx omnara profiles update <agent-profile-id> --file ./x-signal-agent.yaml --json`

   Note the profile's `id` and `current_config_id` from the output.

## 6. Run a first scan

```sh
npx omnara agents launch --profile <agent-profile-id> --config <current-config-id> \
  --message "Run the X scan now." --json
```

Give the user the link to watch it work:
`https://app.omnara.com/projects/<project-id>/agents/<agent-id>`. A scan
takes a minute or two. The user can reply in the console to ask for reply
drafts or push back on the filtering.

## 7. Choose where to use it

Ask where the user wants to talk to the agent. Any combination works, and the
Omnara console always does: every agent launched from the profile shows up
there.

- **Their own app (recommended):** inside their product or internal tool, or a
  small UI built for it.
- **Slack or Discord:** the team mentions the bot, and replies in the thread go
  to the same agent.

Set up whichever the user picks with [`integrations.md`](../integrations.md)
(read it from
https://raw.githubusercontent.com/omnara-ai/omnara/main/examples/integrations.md
if you don't have the repo). For this agent, name the integrations
`x-signal-agent-slack` and `x-signal-agent-discord`, call the bot "X Signal
Agent", and have the user try it with "@X Signal Agent run the scan".

## 8. Run it daily (optional)

A schedule works with any of the above. Ask whether the user wants a daily
scan, and for the time and timezone (default: weekdays at 9am in the user's
timezone). Set `--cron` and `--timezone` from their answer:

```sh
npx omnara crons create --name x-signal-agent-daily \
  --target-type profile --target-agent-profile-id <agent-profile-id> \
  --cron '0 9 * * 1-5' --timezone America/Los_Angeles \
  --message-template 'Run the daily X scan.' --json
```

If `npx omnara crons list --name x-signal-agent-daily --json` already shows a
trigger, change it with `npx omnara crons update <cron-trigger-id>` (same
flags) instead of creating a second one.

Each firing launches a fresh agent from the profile. It shows up in the console,
and the user's app can pick it up through the SDK or API. To post every digest
in one Slack or Discord thread instead, see "Scheduled runs in a channel" in
`integrations.md`.

## 9. Wrap up

Summarize what you created: the secret, the profile, and any integrations or
cron trigger. Remind the user to delete `.env` or move what's in it somewhere
safe. Then tell them:

- **Change the topic or anything else:** ask a coding agent with this skill.
  It reuses the secret and updates the profile in place.
- **Remove it:** delete the cron trigger (`npx omnara crons delete <id>`),
  remove its integrations, if any (see "Removing" in `integrations.md`),
  then run `npx omnara profiles delete <agent-profile-id>` and
  `npx omnara secrets delete <secret-id>`.
