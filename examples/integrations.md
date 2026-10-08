# Connecting an agent to the user's tools

The example skills link here once their agent profile exists. The skill gives
you the names to use; this file covers what each integration needs. Adapt it
to the user: set up only what they pick, in any order, and use
`npx omnara <command> --help` and [docs.omnara.com](https://docs.omnara.com)
for anything not covered here.

## Before you start

- **Names.** Integration names are permanent, unique in a project, and name
  the agent's tools (`int__<name>__post_message`). Use the names the skill
  gives you; without a skill, use `<profile-name>-slack`, `-discord`, or
  `-github`.
- **Reuse.** Check `npx omnara integrations list --json` first. Reuse an
  integration with the expected name whatever its state, or for Slack, any
  `slack_thread` integration whose launcher already offers this profile
  (earlier versions of the skills named it differently), and add the profile
  to its launcher if it's missing. `disconnected` means setup stopped partway,
  so finish it rather than create another. `active` means Omnara's side is
  connected; the steps marked *in the provider* below happen outside Omnara,
  so confirm those with the user.
- **Launchers.** A launcher decides which profiles a mention can start. Slack
  and Discord offer a list in `settings.launcher.profiles`;
  `npx omnara integrations profiles <integration-id> --profile-ids <id>`
  replaces the whole list, so repeat any already there. GitHub starts exactly
  one profile, `settings.launcher.profile`.
- **Credentials.** Have the user put tokens in `.env` (pasting them in the chat
  also works), and load it with `set -a && . ./.env && set +a` in the same
  command that uses them. Reuse a secret that already exists under the expected
  name. The CLI can't change a secret's value, so if the token changed, the user
  pastes the new one on the dashboard's **Secrets** page (the ID stays the
  same). When setup is done, remind the user to delete `.env` or move what's in
  it somewhere safe.
- **Omnara URL.** The URLs below use `https://app.omnara.com`. On a
  self-hosted Omnara, use its public URL.

## Their own app

Put the agent where the user's team already works: inside their product or
internal tool, or a small UI built for it. Launch agents from the profile and
stream their replies with the TypeScript SDK (`@omnara/sdk`) or the
[REST API](https://docs.omnara.com/api-reference/openapi.yaml). Anything else
that can call the API, such as a webhook, a CI job, or a script, can start the
agent the same way. The app needs an org API key: the user creates one on the
dashboard's **API Tokens** page (**Organization** tab → **New token**), then
grants it a role on this project from the key's detail panel.

## Slack

The bot answers wherever it's mentioned, and replies in that thread go to the
same agent.

1. The user generates an app configuration token at
   [api.slack.com/apps](https://api.slack.com/apps) (**Your App Configuration
   Tokens** → **Generate Token**) and adds it to `.env` as
   `SLACK_APP_CONFIG_TOKEN`. It expires after about 12 hours.
2. Create the integration with the profile in its launcher:

   ```sh
   npx omnara integrations create --name <name> --integration-kind slack_thread \
     --settings '{"launcher":{"profiles":["<agent-profile-id>"]}}' --json
   ```

3. Create and connect the Slack app. The app name is how the team mentions
   the bot, so ask before changing the one the skill suggests:

   ```sh
   npx omnara integrations slack <integration-id> --app-name "<Bot Name>" \
     --app-configuration-token "$SLACK_APP_CONFIG_TOKEN"
   ```

   It opens a Slack authorization page for the user to approve. This finishes
   everything on Slack's side, so a `disconnected` integration resumes here
   (with a new token if the old one expired).
4. *In the provider:* the user invites the bot to a channel
   (`/invite @<Bot Name>`) and mentions it with the skill's first message.

## Discord

The bot answers in a new thread wherever it's mentioned in a server channel,
and replies in the thread go to the same agent. It doesn't answer direct
messages, and attachments are limited to 8 MiB.

1. The user creates an application in the
   [Discord Developer Portal](https://discord.com/developers/applications),
   named as the skill suggests. From **General Information** they copy the
   **Application ID** and **Public Key**. Under **Bot** they select **Reset
   Token** to get the bot token, turn on **Message Content Intent**, and turn
   off **Public Bot** so only they can add it (if Discord refuses, first set
   **Installation** → **Install Link** to **None**). They add
   `DISCORD_APPLICATION_ID`, `DISCORD_PUBLIC_KEY`, and `DISCORD_BOT_TOKEN` to
   `.env`. Resetting the token again invalidates the stored one.
2. Store the token as a project secret named `<name>-bot-token`, unless
   `npx omnara secrets list --owner-kind project --owner-project-id <project-id> --name <name>-bot-token --json`
   already finds one:

   ```sh
   npx omnara secrets create --owner-kind project --owner-project-id <project-id> \
     --name <name>-bot-token --material-kind generic \
     --material-value "$DISCORD_BOT_TOKEN" --json
   ```

3. Create the integration with the profile in its launcher, and note its
   `setup_revision`:

   ```sh
   npx omnara integrations create --name <name> --integration-kind discord_thread \
     --settings '{"launcher":{"profiles":["<agent-profile-id>"]}}' --json
   ```

4. Connect the bot. Omnara checks the token and finds the bot's user. For an
   existing integration, take the current `setup_revision` from
   `npx omnara integrations get <integration-id> --json`:

   ```sh
   npx omnara integrations configure <integration-id> \
     --expected-setup-revision <setup-revision> \
     --provider-tenant-id "$DISCORD_APPLICATION_ID" \
     --credential-secret-id <secret-id> \
     --provider-config "{\"public_key\":\"$DISCORD_PUBLIC_KEY\"}" --json
   ```

5. *In the provider:* under **General Information**, the user sets
   **Interactions Endpoint URL** to
   `https://app.omnara.com/api/integrations/discord/<application-id>/interactions`
   and saves. Buttons on the bot's questions need it. Discord checks the URL
   when it's saved, so this comes after step 4.
6. *In the provider:* the user adds the bot to their server with
   `https://discord.com/oauth2/authorize?client_id=<application-id>&scope=bot&permissions=309237746752&integration_type=0`,
   which asks only for the permissions it needs, then mentions it in a
   channel with the skill's first message. For a private channel, they add
   the bot under the channel's **Permissions**.

## GitHub pull requests

Someone with write access mentions the GitHub App in a pull request comment,
and the agent works on that pull request. Use it only for an agent whose
instruction covers pull requests, such as the coding agent: it replies in PR
comments, and its questions wait in the Omnara console.

1. Create the integration with the profile in its launcher:

   ```sh
   npx omnara integrations create --name <name> --integration-kind github_pr \
     --settings '{"launcher":{"trigger":"mention","profile":"<agent-profile-id>"}}' --json
   ```

   `"mention"` starts the agent only when someone mentions the App. `"both"`
   also starts one for every new pull request, so ask before using it.
   `npx omnara integrations update <integration-id> --settings '...'` changes
   it later and replaces the whole `settings` object.
2. Registering the App needs the browser. The user opens
   `https://app.omnara.com/projects/<project-id>/integrations/<integration-id>`,
   picks the account that owns the repository, selects **Continue to
   GitHub**, and names the App. Names are unique across GitHub, so suggest
   one with the team's name, such as "Acme Coding Agent"; its slug
   (`acme-coding-agent`) is how the team mentions it. Back in Omnara, they
   select **Choose repositories on GitHub**, grant only the repositories the
   agent works on, and select **Connect integration**. An organization's
   owners may need to approve the installation. A `disconnected` integration
   resumes on this page, which shows what's left.
3. *In the provider:* Omnara registers the App with read-only access to code.
   For the agent to push to a pull request's branch, the user opens the App's
   settings on GitHub (**Settings** → **Developer settings** → **GitHub Apps**
   → the App → **Edit**, under the organization's settings if it owns the
   App), sets **Permissions & events** → **Repository permissions** →
   **Contents** to **Read and write**, and saves. Pushes fail until whoever
   installed the App accepts the change. On the App's **General** page, they
   also check that **Webhook URL** is
   `https://app.omnara.com/api/integrations/github/events`, and correct it if
   not; otherwise mentions don't reach Omnara.
4. The user mentions `@<app-slug>` in a comment on an open pull request.

## Scheduled runs in a channel

A cron trigger that targets the profile starts a new agent for each run, which
shows up in the console and the user's app. To have every run post in one
Slack or Discord thread instead, have the user mention the bot there once,
then create the trigger with `--target-type agent --target-agent-id <agent-id>`,
taking the agent ID from that conversation's console URL.

## Removing

Delete an integration with `npx omnara integrations delete <integration-id>`.
If other agents share it, rerun `npx omnara integrations profiles` with only
their profiles instead. For Discord, also delete the bot token secret and the
application in the Developer Portal; for GitHub, the App in GitHub's settings.
