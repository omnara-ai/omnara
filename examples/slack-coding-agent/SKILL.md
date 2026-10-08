---
name: slack-coding-agent
description: Deploy or update the Slack Coding Agent on Omnara, a coding agent people mention in Slack, in Discord, or on a GitHub pull request that works on a GitHub repository and opens pull requests. Use when the user asks to deploy, set up, update, or remove the Slack coding agent.
---

# Deploy the Slack Coding Agent

You are setting up an Omnara agent for the user from `agent.yaml` in this
folder. The goal: a coding agent the user's team can mention in Slack, in
Discord, or on a GitHub pull request, which works on one of their GitHub
repositories and opens pull requests, and which the user has watched answer
once.

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
the integrations the user picks in step 5. Slack and Discord setup is
simplest with the CLI; registering a GitHub App needs the Omnara dashboard.

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
   Suggest `anthropic/claude-opus-5.5` on `omnara-openrouter` if it's granted
   (`agent.yaml` runs it at high reasoning effort); otherwise suggest the strongest
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
   `high`: remove the `reasoning` block and retry. Note the profile `id`.

## 5. Choose where the team reaches it

Ask where the team should mention the agent: Slack, Discord, GitHub pull
requests, or any combination. Wherever it's mentioned, it answers in that
thread or pull request, and replies there go to the same agent.

**Slack.** If `npx omnara integrations list --json` already has an integration
named `slack-coding-agent-slack` from an earlier run of this skill, reuse it
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
   npx omnara integrations create --name slack-coding-agent-slack \
     --integration-kind slack_thread \
     --settings '{"launcher":{"profiles":["<agent-profile-id>"]}}' --json
   ```

   The name is permanent and names the agent's Slack tools
   (`int__slack-coding-agent-slack__post_message`).
3. Create the Slack app and connect it:

   ```sh
   set -a && . ./.env && set +a
   npx omnara integrations slack <integration-id> --app-name "Coding Agent" \
     --app-configuration-token "$SLACK_APP_CONFIG_TOKEN"
   ```

   Ask before choosing a different app name; it's how the team mentions the
   bot. It opens a Slack authorization page that the user approves before it
   expires.
4. Tell the user to invite the bot to a channel (`/invite @Coding Agent`).
   A private channel suits it, since threads often include internal details.

**Discord.** The bot answers in a new thread wherever it's mentioned in a
server channel; it doesn't answer direct messages.

If an integration named `slack-coding-agent-discord` already exists, reuse it
the same way: add this profile if it's missing. If it's `active`, skip to
step 5; Omnara can't tell whether steps 5 and 6 were done in Discord, so check them
with the user. If it's `disconnected`, skip step 3 and do the steps that weren't
done, passing the current `setup_revision` from
`npx omnara integrations get <integration-id> --json` in step 4.

1. The user creates an application named "Coding Agent" in the
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
   `slack-coding-agent-discord-bot-token` already exists
   (`npx omnara secrets list --owner-kind project --owner-project-id <project-id> --name slack-coding-agent-discord-bot-token --json`),
   reuse its ID; if the user has reset the token since, have them paste the new
   one into it on the dashboard's **Secrets** page (the ID stays the same).
   Otherwise:

   ```sh
   set -a && . ./.env && set +a
   npx omnara secrets create --owner-kind project --owner-project-id <project-id> \
     --name slack-coding-agent-discord-bot-token --material-kind generic \
     --material-value "$DISCORD_BOT_TOKEN" --json
   ```

3. Create the integration with this profile in its launcher, and note the
   `itg_…` ID and `setup_revision` it returns:

   ```sh
   npx omnara integrations create --name slack-coding-agent-discord \
     --integration-kind discord_thread \
     --settings '{"launcher":{"profiles":["<agent-profile-id>"]}}' --json
   ```

   The name is permanent and names the agent's Discord tools
   (`int__slack-coding-agent-discord__post_message`).
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
   A private channel suits it, since threads often include internal
   details; the user adds the bot to it under the channel's **Permissions**.

**GitHub pull requests.** Someone with write access mentions the GitHub App
in a pull request comment, and the agent works on that pull request: it
reviews it, answers in comments, or pushes fixes to its branch.

If an integration named `slack-coding-agent-github` already exists, reuse it
whatever its state: if `npx omnara integrations get <integration-id> --json`
doesn't show this profile under `settings.launcher.profile`, set it with the
`update` command below. Then skip to step 3 if it's `active`, since Omnara can't
tell whether it was done on GitHub, or to step 2 if it's `disconnected`; the
integration's page shows what's left.

1. Create the integration with this profile in its launcher, and note the
   `itg_…` ID it returns:

   ```sh
   npx omnara integrations create --name slack-coding-agent-github \
     --integration-kind github_pr \
     --settings '{"launcher":{"trigger":"mention","profile":"<agent-profile-id>"}}' --json
   ```

   The name is permanent and names the agent's pull request tools
   (`int__slack-coding-agent-github__discussion_comment`). `"mention"` starts
   the agent only when someone mentions the App; `"both"` also starts one for
   every new pull request, so ask before using it. To change it later, run
   `npx omnara integrations update <integration-id> --settings '...'` with the
   whole `settings` object.
2. Registering the GitHub App happens in the browser. Have the user open
   `https://app.omnara.com/projects/<project-id>/integrations/<integration-id>`,
   pick the account that owns `GITHUB_REPO`, and select **Continue to
   GitHub**. On GitHub they name the App. Names are unique across GitHub, so
   suggest one with the team's name, such as "Acme Coding Agent"; its slug
   (`acme-coding-agent`) is how the team mentions it. Back in Omnara they
   select **Choose repositories on GitHub**, grant only `GITHUB_REPO`, and
   then **Connect integration**. An organization's owners may need to approve
   the installation first.
3. Omnara gives the App read-only access to code, so it can't push fixes yet.
   In the App's settings on GitHub (**Settings** → **Developer settings** →
   **GitHub Apps** → the App → **Edit**, under the organization's settings if
   an organization owns it), the user opens **Permissions & events**, sets
   **Repository permissions** → **Contents** to **Read and write**, and saves.
   Whoever installed the App then accepts the new permission on the
   installation's settings page; until then pushes fail. On the App's
   **General** page, also check that **Webhook URL** is
   `https://app.omnara.com/api/integrations/github/events`, and correct it if
   not; mentions don't reach Omnara otherwise.
4. Tell the user to comment on an open pull request in `GITHUB_REPO`:
   "@acme-coding-agent summarize this PR", with their App's slug.

## 6. Give it a first task

Have the user mention the bot in Slack or Discord with a question that doesn't
change anything:

> @Coding Agent what does this repo do, and how do I run its tests?

The first reply takes a minute or two while the machine starts and clones the
repository. The conversation also shows up as an agent in the Omnara console
([app.omnara.com](https://app.omnara.com)), where the user can watch every
command it runs. Once the answer looks right, suggest a small real fix as the
next message, such as a known bug or a typo, so they see it open a pull
request. With only GitHub connected, the comment from step 5 is the first
task.

## 7. Wrap up

Summarize what you created: the secret, the profile, and the Slack, Discord,
or GitHub integrations. Then tell the user:

- **Start it from elsewhere too:** anything that can call the API (a Linear
  webhook, a CI job) can launch an agent from the profile with the task as
  the message; see [docs.omnara.com](https://docs.omnara.com). The
  instruction assumes a Slack or Discord thread or a pull request, so for
  those entry points adjust its Reporting Guidelines.
- **Change the instruction, model, or anything else:** ask a coding agent
  with this skill. It reuses the secret and updates the profile in place.
- **Remove it:** delete the Slack, Discord, and GitHub integrations, if any
  (`npx omnara integrations delete <integration-id>`), or, if other agents
  share a Slack or Discord one, rerun `npx omnara integrations profiles` with
  only their profiles. Then run `npx omnara profiles delete <agent-profile-id>`
  and `npx omnara secrets delete <secret-id>` for each secret. Revoke the
  GitHub token in GitHub's settings as well, and delete the Discord
  application or GitHub App if there is one.
