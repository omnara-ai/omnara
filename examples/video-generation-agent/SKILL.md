---
name: video-generation-agent
description: Deploy or update the Video Generation Agent on Omnara, an agent that turns a brief into a short video with Remotion, reviews its own frames, and delivers the rendered MP4. Use when the user asks to deploy, set up, update, or remove the video generation agent.
---

# Deploy the Video Generation Agent

You are setting up an Omnara agent for the user from `agent.yaml` in this
folder. The goal: an agent profile the user has watched produce one video,
reachable wherever they want it (their own app, Slack, Discord, or the
Omnara console).

The steps below are the usual path and have the exact commands. Skip anything
that's already done (for example, the skill and profile exist and the user
only wants a different model), follow the user's lead if they want a
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
the Remotion skill uploaded, a profile from the filled-in `agent.yaml`, and
whatever the user picks in step 6. Slack and Discord setup is simplest with
the CLI.

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

## 2. Upload the Remotion skill

The agent writes videos with [Remotion](https://www.remotion.dev) and follows
Remotion's own [agent skills](https://github.com/remotion-dev/skills).
`remotion-best-practices` is a router: its `SKILL.md` points to reference
folders next to it, so upload the whole folder, not just `SKILL.md`.

1. Check for an existing upload:
   `npx omnara skills list --owner-kind project --owner-project-id <project-id> --name remotion-best-practices --json`.
   If one exists, ask whether to reuse it as is or refresh it with the latest
   Remotion release. To reuse it, note its `id` and skip to step 3. To
   refresh it, run the upload below: it adds a revision and the ID stays the
   same.
2. Clone the skills and upload the folder:

   ```sh
   git clone --depth 1 https://github.com/remotion-dev/skills /tmp/remotion-skills
   npx omnara skills create --owner-kind project --owner-project-id <project-id> \
     --archive /tmp/remotion-skills/skills/remotion-best-practices --json
   ```

   The CLI packs the directory itself. Note the `id` (`skl_…`) from the
   output; it's `REMOTION_SKILL_ID`.

To pick up a newer Remotion release later, clone again and rerun the same
`skills create` command. It uploads a new revision under the same `skl_` ID,
and the agent uses it from its next model call, so the profile doesn't change.

## 3. Pick the model and machine pool

1. `npx omnara grant models list --json`: each item has
   `model.provider_config` and `model.name`. Suggest Claude Opus 5.5 (the
   instruction was tested with it, and it reviews its own frames well). If the
   user has their own Anthropic provider config with Opus 5.5, suggest that
   one; otherwise suggest `anthropic/claude-opus-5.5` on `omnara-openrouter`.
   If Opus 5.5 isn't granted, suggest another strong model that accepts
   images, and name a couple of alternatives rather than the whole list. The
   choice gives `MODEL_PROVIDER_CONFIG` and `MODEL_NAME`.
2. `npx omnara grant pools list --json`; each grant's `machine_pool.name` is a
   candidate `MACHINE_POOL`. Suggest `default-pool` if it's granted: its
   machines come with Node.js and headless Chromium, which Remotion needs.
   Otherwise use the only pool, or ask if there are several. If there are
   none, tell the user the project needs a machine pool; the agent can't
   render without one.

## 4. Create the agent

1. Copy `agent.yaml` from this folder to `./video-generation-agent.yaml` in
   the current directory. If this folder isn't available locally, download
   https://raw.githubusercontent.com/omnara-ai/omnara/main/examples/video-generation-agent/agent.yaml
   instead.
2. In the copy, replace every placeholder:

   | Placeholder | Value |
   | --- | --- |
   | `{{REMOTION_SKILL_ID}}` | the `skl_…` ID from step 2 |
   | `{{MODEL_PROVIDER_CONFIG}}` | from step 3 |
   | `{{MODEL_NAME}}` | from step 3 |
   | `{{MACHINE_POOL}}` | from step 3 |

   Then confirm nothing is left: `grep -n '{{' video-generation-agent.yaml`
   must print nothing.
3. Check for an existing profile: `npx omnara profiles list --name video-generation-agent --json`.
   - None: `npx omnara profiles create --name video-generation-agent --file ./video-generation-agent.yaml --json`
   - Exists: `npx omnara profiles update <agent-profile-id> --file ./video-generation-agent.yaml --json`

   If it rejects the reasoning effort, the chosen model doesn't support
   `xhigh`: change it to `high`, or remove the `reasoning` block, and retry.
   Note the profile's `id` and `current_config_id` from the output.

## 5. Make a first video

Ask the user for a brief. If they don't have one in mind, use this:

```sh
npx omnara agents launch --profile <agent-profile-id> --config <current-config-id> \
  --message "A 10-second 16:9 title card for a product launch: the words 'Launch day' animate in over a slow-moving starfield, hold, then fade to black." --json
```

Give the user the link to watch it work:
`https://app.omnara.com/projects/<project-id>/agents/<agent-id>`. The first
video usually takes several minutes: the agent sets up a Remotion project,
renders, checks its own frames, and renders again if it finds problems. The
MP4 arrives as a file in the conversation. The user can reply there to ask
for changes ("make the text bigger", "make it 9:16"), or attach a logo or
music for the agent to use.

## 6. Choose where to use it

Ask where the user wants to talk to the agent, offering these in this order.
Any combination is fine.

**Their own app, existing or new (recommended).** Put the agent where the
user's team already works: inside their existing product or internal tool, or, if there's no
natural home, a small UI built for it. Both are the same integration: launch
agents from the profile and stream their replies with the TypeScript SDK
(`@omnara/sdk`) or the REST API; see [docs.omnara.com](https://docs.omnara.com).
Anything else that can call the API (a CMS hook, a CI job, a script) can
start the agent the same way, with the brief as the message. The app needs an
org API key: the user creates one on the dashboard's **API Tokens** page
(**Organization** tab → **New token**), then grants it a role on this project
from the key's detail panel. Help them wire it in, or build the UI with them.

**Slack.** The bot answers wherever it's mentioned and posts the MP4 in the
thread, and thread replies become revision requests.

If `npx omnara integrations list --json` already has an integration named
`video-generation-agent-slack` from an earlier run of this skill, reuse it
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
   npx omnara integrations create --name video-generation-agent-slack \
     --integration-kind slack_thread \
     --settings '{"launcher":{"profiles":["<agent-profile-id>"]}}' --json
   ```

   The name is permanent and names the agent's Slack tools
   (`int__video-generation-agent-slack__post_message`).
3. Create the Slack app and connect it:

   ```sh
   set -a && . ./.env && set +a
   npx omnara integrations slack <integration-id> --app-name "Video Agent" \
     --app-configuration-token "$SLACK_APP_CONFIG_TOKEN"
   ```

   Ask before choosing a different app name; it's how the team mentions the
   bot. It opens a Slack authorization page that the user approves before it
   expires.
4. Tell the user to invite the bot to a channel (`/invite @Video Agent`) and
   mention it with a brief: "@Video Agent a 15-second teaser for our new
   pricing page".

**Discord.** Works like Slack: the bot answers in a new thread wherever
it's mentioned in a server channel, and replies in the thread go to the
same agent. It doesn't answer direct messages.

If an integration named `video-generation-agent-discord` already exists, reuse
it the same way: add this profile if it's missing, and if it's `active`, skip to
its last step. If it's `disconnected`, skip step 3 and do the steps that weren't
done, passing the current `setup_revision` from
`npx omnara integrations get <integration-id> --json` in step 4.

1. The user creates an application named "Video Agent" in the
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
   `video-generation-agent-discord-bot-token` already exists
   (`npx omnara secrets list --owner-kind project --owner-project-id <project-id> --name video-generation-agent-discord-bot-token --json`),
   reuse its ID; if the user has reset the token since, have them paste the new
   one into it on the dashboard's **Secrets** page (the ID stays the same).
   Otherwise:

   ```sh
   set -a && . ./.env && set +a
   npx omnara secrets create --owner-kind project --owner-project-id <project-id> \
     --name video-generation-agent-discord-bot-token --material-kind generic \
     --material-value "$DISCORD_BOT_TOKEN" --json
   ```

3. Create the integration with this profile in its launcher, and note the
   `itg_…` ID and `setup_revision` it returns:

   ```sh
   npx omnara integrations create --name video-generation-agent-discord \
     --integration-kind discord_thread \
     --settings '{"launcher":{"profiles":["<agent-profile-id>"]}}' --json
   ```

   The name is permanent and names the agent's Discord tools
   (`int__video-generation-agent-discord__post_message`).
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
   Then they mention it in a channel: "@Video Agent a 15-second teaser for
   our new pricing page".

**Omnara console.** Already done: every agent launched from the profile shows
up in the console.

## 7. Wrap up

Summarize what you created: the skill, the profile, and the Slack or Discord
integration if any.
Then tell the user:

- **Change the instruction, model, or anything else:** ask a coding agent
  with this skill. It reuses the skill and updates the profile in place.
- **Remove it:** delete the Slack and Discord integrations, if any
  (`npx omnara integrations delete <integration-id>`), or, if other agents
  share it, rerun `npx omnara integrations profiles` with only their
  profiles. Then run `npx omnara profiles delete <agent-profile-id>` and
  `npx omnara skills delete <skill-id>`.
  With Discord, also delete the bot token secret
  (`npx omnara secrets delete <secret-id>`) and the application in the
  Discord Developer Portal.
