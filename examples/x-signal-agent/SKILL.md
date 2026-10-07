---
name: x-signal-agent
description: Deploy or update the X Signal Agent on Omnara, an agent that searches X for posts about a topic and delivers a daily digest. Use when the user asks to deploy, set up, update, or remove the X signal agent.
---

# Deploy the X Signal Agent

You are setting up an Omnara agent for the user from `agent.yaml` in this
folder. The goal: an agent profile the user has watched run once, reachable
wherever they want it (their own app, Slack, or the Omnara console), and
optionally running on a daily schedule.

The steps below are the usual path and have the exact commands. Skip anything
that's already done (for example, the token and profile exist and the user
only wants a new topic), follow the user's lead if they want a different
order, and narrate briefly as you go.

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
a secret with the token, a profile from the filled-in `agent.yaml`, and
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

1. Check for an existing secret:
   `npx omnara secrets list --owner-kind project --owner-project-id <project-id> --name x-signal-agent-bearer-token --json`.
   If one exists, ask whether to reuse it. To reuse it, note its `id` and skip
   to step 4. To change its value, have the user update it on the
   **Secrets** page of the Omnara dashboard (the ID stays the same) and skip to
   step 4.
2. Ask the user to add the token to a `.env` file in the current directory
   and tell you when it's saved (pasting it in the chat also works):

   ```sh
   X_BEARER_TOKEN=...
   ```

3. Create the secret:

   ```sh
   set -a && . ./.env && set +a
   npx omnara secrets create --owner-kind project --owner-project-id <project-id> \
     --name x-signal-agent-bearer-token --material-kind generic \
     --material-value "$X_BEARER_TOKEN" --json
   ```

   Note the `id` (`sec_…`) from the output.

## 4. Pick the machine pool and model

1. `npx omnara grant pools list --json`: each grant's `machine_pool.name` is a
   candidate `MACHINE_POOL`; the agent runs `curl` on a machine from this
   pool. Use the only one, or ask if there are several. If there are none,
   stop and tell the user the project needs a machine pool.
2. `npx omnara grant models list --json`: each item has `model.provider_config`
   and `model.name`. Ask the user which model to use. Suggest
   `openai/gpt-6.1-sol` on `omnara-openrouter` if it's granted (it replaces
   GPT-6 Sol, which the instruction was tested with); otherwise suggest a
   strong general-purpose model from
   the list. Name a couple of alternatives rather than the whole list, and show
   everything if they ask. The choice gives `MODEL_PROVIDER_CONFIG` and
   `MODEL_NAME`.

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
instructions to the agent.

If `npx omnara integrations list` already shows an active `slack_thread`
integration for this agent, from an earlier run of this skill, reuse it: skip
to step 4, and if `npx omnara integrations get <integration-id>` doesn't list
the profile under `settings.launcher.profiles`, add it with
`npx omnara integrations profiles <integration-id> --profile-ids <agent-profile-id>`.
The flag replaces the offered profiles, so repeat it for each one already listed.

1. The user creates an app configuration token at
   [api.slack.com/apps](https://api.slack.com/apps) under **Your App
   Configuration Tokens** → **Generate Token**. It expires after about 12
   hours. Have the user add it to `.env` as `SLACK_APP_CONFIG_TOKEN=...`.
2. Create the integration with this profile in its launcher, so a mention
   starts this agent, and note the `itg_…` ID it returns:

   ```sh
   npx omnara integrations create --name x-signal-agent \
     --integration-kind slack_thread \
     --settings '{"launcher":{"profiles":["<agent-profile-id>"]}}' --json
   ```

   The name is permanent and names the agent's Slack tools
   (`int__x-signal-agent__post_message`).
3. Create the Slack app and connect it:

   ```sh
   set -a && . ./.env && set +a
   npx omnara integrations slack <integration-id> --app-name "X Signal Agent" \
     --app-configuration-token "$SLACK_APP_CONFIG_TOKEN"
   ```

   Ask before choosing a different app name; it's how the team mentions the
   bot. It opens a Slack authorization page that the user approves before it
   expires.
4. Tell the user to invite the bot to a channel (`/invite @X Signal Agent`)
   and mention it: "@X Signal Agent run the scan".

**Omnara console.** Already done: every agent launched from the profile shows
up in the console.

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

Each firing launches a fresh agent from the profile. It shows up in the
console, and the user's app can pick it up through the SDK or API. To get
each digest in a Slack channel, mention the bot in that channel once, then
create the trigger with `--target-type agent --target-agent-id <agent-id>`
(the agent ID from that conversation's console URL), so every digest posts to
that thread.

## 9. Wrap up

Summarize what you created: the secret name, the profile, and the Slack app or
cron trigger if any. Then tell the user:

- **Change the topic or anything else:** ask a coding agent with this skill.
  It reuses the secret and updates the profile in place.
- **Remove it:** delete the cron trigger (`npx omnara crons delete <id>`),
  delete the Slack integration, if any
  (`npx omnara integrations delete <integration-id>`), or, if other agents
  share it, rerun `npx omnara integrations profiles` with only their
  profiles. Then run `npx omnara profiles delete <agent-profile-id>` and
  `npx omnara secrets delete <secret-id>`.
