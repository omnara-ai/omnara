---
name: reddit-signal-agent
description: Deploy or update the Reddit Signal Agent on Omnara, an agent that scrapes Reddit for posts about a topic and delivers a daily digest. Use when the user asks to deploy, set up, update, or remove the Reddit signal agent.
---

# Deploy the Reddit Signal Agent

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

Run `npx omnara whoami`. If you aren't logged in, run `npx omnara login` without
piping its output, and keep it running: it waits until the user approves. It
opens the approval page in the user's browser and prints the page's link and a
code. Show the user both right away, since you may be on a different machine
than their browser, and ask them to approve once the code matches. When it
exits, `npx omnara whoami` should succeed.

Pick the org and project (`npx omnara whoami --json` lists orgs,
`npx omnara projects list --org <org-id> --json` their projects): use them if
there's one of each, otherwise ask, suggesting the defaults from
`npx omnara config` or the project named `Default`. Save the choice with
`npx omnara config --org <org-id> --project <project-id>`; later steps need the
project ID.

## 2. Choose what to track

Ask the user what they want to hear about on Reddit: their product category,
competitors, customers' pain points, or any topic. Then write three values:

- `TOPIC`: a short phrase the agent judges relevance against, for example
  `managed-agent infrastructure`.
- `REDDIT_SEARCHES`: phrases searched across all of Reddit, as a JSON array on
  one line, for example `["managed agents", "agent infrastructure"]`. Reddit
  search has no boolean operators, so use one plain phrase per entry; three to
  six is plenty.
- `SUBREDDIT_URLS`: communities whose newest posts are scanned even when
  search would miss them, as a JSON array of `/new/` URLs on one line, for
  example
  `[{"url": "https://www.reddit.com/r/AI_Agents/new/"}, {"url": "https://www.reddit.com/r/LLMDevs/new/"}]`.
  Suggest subreddits where the user's audience gathers; up to about ten.

Show all three to the user (as plain lists, not JSON) and adjust until they're
happy.

## 3. Store the Apify token

Reddit's own API requires manual approval, so the agent scrapes through
[Apify's hosted MCP server](https://mcp.apify.com) running the
[trudax/reddit-scraper-lite](https://apify.com/trudax/reddit-scraper-lite)
scraper. The user needs a free Apify account
([console.apify.com/sign-up](https://console.apify.com/sign-up), no credit
card); the API token is on the **API & Integrations** page under
**Settings**. The scraper is billed per result, and each scan is capped at 120
results, so it costs cents.

If a secret named `reddit-signal-agent-apify-token` already exists
(`npx omnara secrets list --owner-kind project --owner-project-id <project-id> --name reddit-signal-agent-apify-token --json`),
ask whether to reuse it and note its `id`; if the value changed, the user
updates it on the dashboard's **Secrets** page (the ID stays the same).
Otherwise have the user add `APIFY_TOKEN=...` to a `.env` file in the current
directory (pasting it in the chat also works), then create the secret and note
its `id` (`sec_…`):

```sh
set -a && . ./.env && set +a
npx omnara secrets create --owner-kind project --owner-project-id <project-id> \
  --name reddit-signal-agent-apify-token --material-kind generic \
  --material-value "$APIFY_TOKEN" --json
```

## 4. Pick the model

`npx omnara grant models list --json`: each item has `model.provider_config` and
`model.name`. Ask the user which model to use, suggesting `openai/gpt-6.1-sol`
on `omnara-openrouter` if it's granted, otherwise a strong general-purpose model
from the list. The choice gives `MODEL_PROVIDER_CONFIG` and `MODEL_NAME`.

This agent needs no machine pool: the Apify MCP server does all the fetching.

## 5. Create the agent

1. Copy `agent.yaml` from this folder to `./reddit-signal-agent.yaml` in the
   current directory. If this folder isn't available locally, download
   https://raw.githubusercontent.com/omnara-ai/omnara/main/examples/reddit-signal-agent/agent.yaml
   instead.
2. In the copy, replace every placeholder:

   | Placeholder | Value |
   | --- | --- |
   | `{{TOPIC}}` | from step 2 |
   | `{{REDDIT_SEARCHES}}` | from step 2, the JSON array |
   | `{{SUBREDDIT_URLS}}` | from step 2, the JSON array |
   | `{{APIFY_TOKEN_SECRET_ID}}` | the `sec_…` ID from step 3 |
   | `{{MODEL_PROVIDER_CONFIG}}` | from step 4 |
   | `{{MODEL_NAME}}` | from step 4 |

   Then confirm nothing is left: `grep -n '{{' reddit-signal-agent.yaml` must
   print nothing.
3. Check for an existing profile: `npx omnara profiles list --name reddit-signal-agent --json`.
   - None: `npx omnara profiles create --name reddit-signal-agent --file ./reddit-signal-agent.yaml --json`
   - Exists: `npx omnara profiles update <agent-profile-id> --file ./reddit-signal-agent.yaml --json`

   Note the profile's `id` and `current_config_id` from the output.

## 6. Run a first scan

```sh
npx omnara agents launch --profile <agent-profile-id> --config <current-config-id> \
  --message "Run the Reddit scan now." --json
```

Give the user the link to watch it work:
`https://app.omnara.com/projects/<project-id>/agents/<agent-id>`. The scrape
runs a real browser behind the MCP server, so a scan takes several minutes,
mostly a quiet stretch of `get-actor-run` calls while the agent waits. The
user can reply in the console to ask for reply drafts or push back on the
filtering.

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
`reddit-signal-agent-slack` and `reddit-signal-agent-discord`, call the bot
"Reddit Signal Agent", and have the user try it with "@Reddit Signal Agent run
the scan".

## 8. Run it daily (optional)

A schedule works with any of the above. Ask whether the user wants a daily
scan, and for the time and timezone (default: weekdays at 9am in the user's
timezone). Set `--cron` and `--timezone` from their answer:

```sh
npx omnara crons create --name reddit-signal-agent-daily \
  --target-type profile --target-agent-profile-id <agent-profile-id> \
  --cron '0 9 * * 1-5' --timezone America/Los_Angeles \
  --message-template 'Run the daily Reddit scan.' --json
```

If `npx omnara crons list --name reddit-signal-agent-daily --json` already
shows a trigger, change it with `npx omnara crons update <cron-trigger-id>`
(same flags) instead of creating a second one.

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
  `npx omnara secrets delete <secret-id>` (skip the secret if another agent
  shares it).
