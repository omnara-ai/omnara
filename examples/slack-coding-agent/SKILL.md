---
name: slack-coding-agent
description: Deploy or update the Slack Coding Agent on Omnara, a coding agent people mention in Slack that works on a GitHub repository and opens pull requests. Use when the user asks to deploy, set up, update, or remove the Slack coding agent.
---

# Deploy the Slack Coding Agent

You are setting up an Omnara agent for the user from `agent.yaml` in this
folder. The goal: a coding agent the user's team can mention in Slack, which
works on one of their GitHub repositories and opens pull requests, and which
the user has watched answer once in Slack.

The steps below are the usual path and have the exact commands. Skip anything
that's already done (for example, the token and profile exist and the user
only wants a different model), follow the user's lead if they want a
different order, and narrate briefly as you go.

- Start from `agent.yaml` as written. If the user wants something different
  (the instruction, tools, or model), change it and tell them what you
  changed.
- If the user wants the agent to read their issue tracker (Linear, Jira, and
  so on), add the tracker's MCP server in an `mcp:` block, store its
  credential with `npx omnara secrets mcp-oauth` (OAuth) or as a `generic`
  secret (API key), and add a short instruction section on how to use it.
  Tell the user that an OAuth connection acts as whoever approves it, so its
  writes appear under that person's name.
- If a command fails, read the error and fix it; `npx omnara <command> --help`
  and [docs.omnara.com](https://docs.omnara.com) have the details. If you're
  stuck, show the user the error.

The commands use the Omnara CLI (`npx omnara`) because its login handles auth
in one step; add `--json` when you need to read IDs from the output. They're a
reference, not a requirement: the Omnara MCP tools, the
[REST API](https://docs.omnara.com/api-reference/openapi.yaml), or the SDK work
too, and the flags map directly to API fields. What matters is the result: a
secret with the GitHub token, a profile from the filled-in `agent.yaml`, and
a Slack app connected to it. Slack setup is simplest with the CLI.

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

## 2. Choose the repository and store a GitHub token

Ask which repository the agent works on (`GITHUB_REPO`, as `owner/name`). One
repository is supported as written.

The agent pushes branches and opens pull requests, so it needs a token with
write access. Recommend a
[fine-grained token](https://github.com/settings/personal-access-tokens/new)
limited to that repository, with **Contents** and **Pull requests** set to
read and write. Optional extras: **Issues** read (to read linked issues),
**Actions** read (to read CI logs), and **Workflows** read and write (only if
it should edit `.github/workflows`). Commits and pull requests appear under
the token owner's account, so a dedicated bot account keeps them separate
from a person's. If the repository belongs to an organization, its owners
may need to approve the token.

1. Check for an existing secret:
   `npx omnara secrets list --owner-kind project --owner-project-id <project-id> --name slack-coding-agent-github --json`.
   If one exists, ask whether to reuse it. To reuse it, note its `id` and skip
   to step 3. To change its value, have the user update it on the
   **Secrets** page of the Omnara dashboard (the ID stays the same) and skip
   to step 3.
2. Ask the user to add the token to a `.env` file in the current directory
   and tell you when it's saved (pasting it in the chat also works):

   ```sh
   AGENT_GITHUB_TOKEN=github_pat_...
   ```

3. Create the secret:

   ```sh
   set -a && . ./.env && set +a
   npx omnara secrets create --owner-kind project --owner-project-id <project-id> \
     --name slack-coding-agent-github --material-kind generic \
     --material-value "$AGENT_GITHUB_TOKEN" --json
   ```

   Note the `id` (`sec_…`) as `GITHUB_TOKEN_SECRET_ID`.

## 3. Pick the model and machine pool

1. `npx omnara grant models list --json`: each item has
   `model.provider_config` and `model.name`. Ask the user which model to use.
   Suggest `anthropic/claude-fable-5.1` on `omnara-openrouter` if it's granted
   (the instruction was tested with it); otherwise suggest the strongest
   coding model on the list. Name a couple of alternatives rather than the
   whole list, and show everything if they ask. The choice gives
   `MODEL_PROVIDER_CONFIG` and `MODEL_NAME`.
2. `npx omnara grant pools list --json`; each grant's `machine_pool.name` is a
   candidate `MACHINE_POOL`. Suggest `default-pool` if it's granted: its
   machines come with `git`, `gh`, and common language toolchains. Otherwise
   use the only pool, or ask if there are several. If there are none, tell
   the user the project needs a machine pool; the agent can't work on code
   without one. On a custom pool, the image needs `git` and `gh`.

## 4. Create the agent

1. Copy `agent.yaml` from this folder to `./slack-coding-agent.yaml` in the
   current directory. If this folder isn't available locally, download
   https://raw.githubusercontent.com/omnara-ai/omnara/main/examples/slack-coding-agent/agent.yaml
   instead.
2. In the copy, replace every placeholder:

   | Placeholder | Value |
   | --- | --- |
   | `{{GITHUB_REPO}}` | from step 2 (it appears twice) |
   | `{{GITHUB_TOKEN_SECRET_ID}}` | from step 2 |
   | `{{MODEL_PROVIDER_CONFIG}}` | from step 3 |
   | `{{MODEL_NAME}}` | from step 3 |
   | `{{MACHINE_POOL}}` | from step 3 |

   Then confirm nothing is left: `grep -n '{{' slack-coding-agent.yaml` must
   print nothing.
3. Check for an existing profile: `npx omnara profiles list --name slack-coding-agent --json`.
   - None: `npx omnara profiles create --name slack-coding-agent --file ./slack-coding-agent.yaml --json`
   - Exists: `npx omnara profiles update <agent-profile-id> --file ./slack-coding-agent.yaml --json`

   If it rejects the reasoning effort, the chosen model doesn't support
   `medium`: remove the `reasoning` block and retry. Note the profile `id`.

## 5. Connect Slack

This agent is built for Slack: it replies in the thread where it's mentioned,
and replies in that thread go to the same agent.

1. The user creates an app configuration token at
   [api.slack.com/apps](https://api.slack.com/apps) under **Your App
   Configuration Tokens** → **Generate Token**. It expires after about 12
   hours.
2. Have the user add it to `.env` as `SLACK_APP_CONFIG_TOKEN=...` (or paste
   it in the chat), then run:

   ```sh
   set -a && . ./.env && set +a
   npx omnara profiles slack <agent-profile-id> --app-name "Coding Agent" \
     --app-configuration-token "$SLACK_APP_CONFIG_TOKEN"
   ```

   It opens a Slack authorization page; the user approves within 10 minutes.
   Ask before choosing a different app name; it's how the team will mention
   the bot.
3. Tell the user to invite the bot to a channel (`/invite @Coding Agent`).
   A private channel suits it, since threads often include internal details.

## 6. Give it a first task

Have the user mention the bot in that channel with a question that doesn't
change anything:

> @Coding Agent what does this repo do, and how do I run its tests?

The first reply takes a minute or two while the machine starts and clones the
repository. The conversation also shows up as an agent in the Omnara console
([app.omnara.com](https://app.omnara.com)), where the user can watch every
command it runs. Once the answer looks right, suggest a small real fix as the
next message, such as a known bug or a typo, so they see it open a pull
request.

## 7. Wrap up

Summarize what you created: the secret, the profile, and the Slack app.
Then tell the user:

- **Start it from elsewhere too:** anything that can call the API (a Linear
  or GitHub webhook, a CI job) can launch an agent from the profile with the
  task as the message; see [docs.omnara.com](https://docs.omnara.com). The
  instruction assumes a Slack thread, so for those entry points adjust its
  Reporting Guidelines.
- **Change the instruction, model, or anything else:** ask a coding agent
  with this skill. It reuses the secret and updates the profile in place.
- **Remove it:** remove the Slack integration from the profile in the
  dashboard, then `npx omnara profiles delete <agent-profile-id>` and
  `npx omnara secrets delete <secret-id>`. Revoke the GitHub token in
  GitHub's settings as well.
