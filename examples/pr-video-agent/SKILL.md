---
name: pr-video-agent
description: Deploy or update the PR Video Agent on Omnara, an agent that records a short video of a pull request's change working in the browser and posts it on the pull request. Use when the user asks to deploy, set up, update, or remove the PR video agent.
---

# Deploy the PR Video Agent

You are setting up an Omnara agent for the user from `agent.yaml` in this
folder. The goal: a GitHub App on one of the user's repositories that
records the change in a pull request working in the app and posts the
recording there, and that the user has watched post one.

The steps below are the usual path, with the exact commands. Skip anything
that's already done (for example, the profile and integration exist and the
user only wants a different trigger), and follow the user's lead if they want a
different order.

- Start from `agent.yaml` as written. If the user wants something different
  (the instruction, tools, or model), change it and tell them what you
  changed.
- If a command fails, `npx omnara <command> --help` and
  [docs.omnara.com](https://docs.omnara.com) have the details.
- Stop only where a step says to ask the user. Ask in a sentence or two of
  plain language, together with the next step's question if one comes right
  after, then end your turn and wait for the reply.
- Where a step gives a default, use it without asking and say in a line what
  you picked, so the user can change it.

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

## 2. Work out how to run the app

Each deployment covers one repository, because the agent is told how to run
that one app. If the user hasn't named the repository and you're working
inside one with a web app, plan on that one.

The agent needs notes on running the app from a fresh clone on a Linux
machine. If you can read the repository, work them out from its README,
package scripts, Makefile, and `AGENTS.md`. Otherwise you'll ask in step 4.
The notes become `APP_NOTES`: a few plain lines covering

- the install command, and the runtime version if it matters
- the command that starts the app, the line it prints when it's ready, and
  the URL to open
- how to sign in, if there is a sign-in: a seeded test account, a sign-up
  the agent does itself, or a username and password from environment
  variables
- how to put data back to a known state, if the app keeps any
- anything it needs that isn't in the repository, such as a database, and
  how to start it

An app that needs only its own code is the easy case. For anything else,
keep to what can run on the machine itself: a local database, seed scripts,
and stubbed outside services. Machines from `default-pool` already have the
tools listed under
[Default machine environment](https://docs.omnara.com/machines/pools#default-machine-environment),
including PostgreSQL and Redis, which are installed but not running; prefer
those to Docker. Port 8080 on those machines is already in use, so if the
app listens there, have the notes start it on another port.

You're writing these notes without running them, so keep a list of the
lines you couldn't verify. The first recording is their real test.

If the app needs environment variables, plain values go in `env_overlay`
under `machine_sources` in step 5. Names that start with `OMNARA_` are
reserved and can't be set there: write those into the notes as `export`
lines for the agent to run. For secret values, such as a test account's
password, the user creates each as an Omnara secret themselves and gives
you its `sec_…` ID:

```sh
npx omnara secrets create --owner-kind project --owner-project-id <project-id> \
  --name pr-video-agent-<name> --material-kind generic --material-value '<value>' --json
```

Those go in `secret_env_overlay`. The pull request's own code runs on this
machine and can read every variable on it, so it gets test credentials
only, never production ones.

## 3. Pick the model and machine pool

1. `npx omnara grant models list --json`: each item has `model.provider_config`
   and `model.name`. The agent looks at screenshots, so the model must accept
   images. Use `anthropic/claude-sonnet-5.5` on `omnara-openrouter` if it's
   granted, otherwise `anthropic/claude-opus-5.5`. The listing doesn't show
   which models accept images, so if neither is granted, ask the user. The
   choice gives `MODEL_PROVIDER_CONFIG` and `MODEL_NAME`.
2. `npx omnara grant pools list --json`; each grant's `machine_pool.name` is a
   candidate `MACHINE_POOL`. Use `default-pool` if it's granted. Otherwise use
   the only pool, or ask if there are several. A custom pool's image needs
   `git`, Node.js, `apt-get` with root, and an up-to-date `omnarad`, which
   supplies the Git credentials.
3. Use `8192` for `MACHINE_MEMORY_MB`. If the grant's `max_machine_memory_mb`
   is set and lower, use that; `null` means no limit. Chrome, the app, and
   its build share it.

## 4. Ask the user

Ask these in one message, with the defaults, so the user can answer
"defaults" or change only what they care about. Leave out anything they've
already told you. Then wait for the reply.

1. **Which repository**, if it isn't settled.
2. **When it records**, which becomes the launcher's `trigger`. Default
   `both`: it looks at every new pull request opened by someone with write
   access and records the ones that change something visible, and anyone
   with write access can mention it on any pull request. The alternatives
   are `pull_request_opened` (new pull requests only) and `mention` (only
   when someone writes, say, "@acme-pr-video record this"). Each pull
   request starts its own agent and machine, so `mention` costs least. Pull
   requests from bots or people without write access never start one on
   their own, and it refuses pull requests from forks whoever asks: their
   code would run next to credentials that can write to the repository.
3. **GIF or video.** Default: a GIF. GitHub gives Apps no way to attach a
   video, so the agent commits a GIF to a hidden ref in the repository and
   embeds it in its comment. This needs no token, and the comment comes from
   the App, but the App must be allowed to push. The alternative is a
   playable video: the user creates a personal access token, and the comment
   is posted from that person's account. Whoever can see the repository can
   see the recording, so on a public repository it's public; check with
   `gh repo view <owner>/<name> --json visibility` and say so. `README.md`
   has the trade-offs under "GIF or video"; show them if the user asks.
4. **New pushes.** Default: it looks at each push to a pull request it
   recorded, and records again only when the push changes something a
   reviewer would see. The alternative is staying quiet after the first
   recording until someone mentions it.
5. **The App's name.** This agent needs its own GitHub App: an integration
   launches one profile, so it can't share another agent's, such as the PR
   review agent's. Suggest a name with the team's name, such as
   "Acme PR Video"; its slug (`acme-pr-video`) is how the team mentions it.
6. **The run notes.** Show the notes from step 2 and say which lines you
   couldn't verify, so the user can correct what they know is wrong. If you
   couldn't read the repository, ask for the items in step 2 instead.

## 5. Create the agent

1. Copy `agent.yaml` from this folder to `./pr-video-agent.yaml` in the
   current directory. If this folder isn't available locally, download
   https://raw.githubusercontent.com/omnara-ai/omnara/main/examples/pr-video-agent/agent.yaml
   instead.
2. In the copy, replace every placeholder:

   | Placeholder | Value |
   | --- | --- |
   | `{{MODEL_PROVIDER_CONFIG}}` | from step 3 |
   | `{{MODEL_NAME}}` | from step 3 |
   | `{{MACHINE_POOL}}` | from step 3 |
   | `{{MACHINE_MEMORY_MB}}` | from step 3 |
   | `{{APP_NOTES}}` | the notes from step 2, as the user corrected them |

   `{{APP_NOTES}}` sits inside the instruction, which is an indented block.
   Put the first line of the notes where the placeholder is, and start
   every later line with two spaces so it lines up with the lines around
   it. Then confirm nothing is left: `grep -n '{{' pr-video-agent.yaml`
   must print nothing.
3. Add any environment variables from step 2 as `env_overlay` and
   `secret_env_overlay` under the `machine_sources` entry.
4. If the user chose video, they need a fine-grained personal access token
   from https://github.com/settings/personal-access-tokens/new with
   **Repository access** set to only this repository and the permissions
   **Contents: Read and write** and **Pull requests: Read and write**. An
   organization may have to approve it. Have them store it as a secret
   themselves, with the command in step 2 and the name
   `pr-video-agent-attach-token`, so the token never appears in this
   conversation. Then uncomment the `secret_env_overlay` lines for
   `VIDEO_ATTACH_TOKEN` in the copy and put the secret's ID there.
5. If the user wants it quiet on new pushes, replace the instruction's
   paragraph that starts "When new commits are pushed" with:

   ```text
   When new commits are pushed, you're told the head changed. Don't post
   anything for that alone; record again only when someone asks, and never
   for a pull request from a fork. To record
   again, first make sure your machine is still there, as in step 2. On the
   same machine, fetch and check out the new head and restart the app. On a
   replacement, do all of step 2 again. Then repeat steps 3 to 5.
   ```
6. Check for an existing profile: `npx omnara profiles list --name pr-video-agent --json`.
   - None: `npx omnara profiles create --name pr-video-agent --file ./pr-video-agent.yaml --json`
   - Exists, for this repository: `npx omnara profiles update <agent-profile-id> --file ./pr-video-agent.yaml --json`
   - Exists, with run notes for a different repository: tell the user, and
     unless they want it replaced, use the names `pr-video-agent-<repo>`
     and `pr-video-agent-<repo>-github` for this one.

   Note the profile `id`, and check that the config in the response still
   has your notes in its instruction.

For a public repository, you can try the notes before GitHub is connected.
Launch one agent from the profile and read what it reports:

```sh
npx omnara agents launch --profile <agent-profile-id> --name "pr-video-agent dry run" --message \
  "Dry run: there is no pull request, so don't call the pull request tools or post anything. Clone https://github.com/<owner>/<name>.git to /workspace/repo, stay on its default branch, and follow your run notes exactly. Record signing in and opening one screen, run video-convert, and look at sheet.png. Don't run video-post. Report which lines of the notes worked as written and the exact replacement for any that didn't."
```

Fix the notes it corrects, update the profile, and archive that agent
(`npx omnara agents archive <agent-id>`). A private repository can't be
cloned until the App is installed, so there the first recording in step 7
is the test.

## 6. Connect GitHub

Set up the GitHub integration with the "GitHub pull requests" section of
[`integrations.md`](../integrations.md) (read it from
https://raw.githubusercontent.com/omnara-ai/omnara/main/examples/integrations.md
if you don't have the repo). That section is written for the coding agent,
so use the settings below in place of the ones in its step 1, and follow
its step 2 for the browser part. For this agent:

- Name the integration `pr-video-agent-github`. Its settings carry the
  trigger from step 4, the profile, and the repository:

  ```json
  {"launcher":{"trigger":"both","profile":"<agent-profile-id>","repository_id":"<id>"},"code_write":true}
  ```

  `repository_id` is the repository's numeric ID as a string.
  `gh api repos/<owner>/<name> --jq .id` prints it; without `gh`,
  `curl -s https://api.github.com/repos/<owner>/<name>` returns it as `id`
  for a public repository. `"code_write": true` is what lets the agent
  store the GIF; leave it out if the user chose video.
- If the integration already exists, bring it up to date before the browser
  step: `npx omnara integrations update <integration-id> --settings '<the JSON above>'`.
- Have the user grant the App **only this repository** when they install
  it. With `code_write` on, the agent's Git credentials can push to every
  repository the installation covers, and the agent runs the pull request's
  code on the same machine, so the installation is what limits it.
- When the user says it's done,
  `npx omnara integrations get <integration-id> --json` should show
  `"state": "active"`. Its `bot_mention` is what the team types to mention
  the App, which differs from your suggestion if that name was taken.

## 7. Watch a first recording

Pick the test from the trigger:

- With `mention` or `both`, have the user comment on an open pull request
  that changes something visible: "@acme-pr-video record this", with their
  App's mention from step 6.
- With `pull_request_opened`, have the user open a pull request with a small
  visible change, such as a button's label.

Omnara adds an 👀 reaction to a comment it accepted, and the pull request
shows a check while the agent works. The agent also shows up in the Omnara
console ([app.omnara.com](https://app.omnara.com)), where the user can watch
it start the app and rehearse. The first recording takes several minutes: a
new machine installs Chrome before anything else. If nothing happens after a
couple of minutes, check the App's webhook URL and its **Advanced** tab on
GitHub, which lists recent deliveries and their responses.

When the comment is up, have the user check that the recording plays for
them on the pull request. If the agent couldn't start the app, its comment
says what failed. Fix the notes from step 2 and update the profile, which
covers pull requests from now on. The agent already on this pull request
keeps the config it started with, so for this one have the user put the
correction in the mention itself, for example "@acme-pr-video record this;
the API now starts with `npm run api`". It follows a mention over its
notes.

To change that agent's config instead, don't give it the profile's file as
it is: the launcher added the pull request tools and Git credentials to
its config, and the file would replace them. Make a copy of
`pr-video-agent.yaml`, add these to it, and run
`npx omnara agents update <agent-id> --file <copy>`
(`npx omnara agents list --agent-profile-id <agent-profile-id>` lists the
agents, one per pull request):

```yaml
tools:  # in place of "tools: {}"
  int__pr-video-agent-github__read: {}
  int__pr-video-agent-github__discussion_comment: {}

git_credentials:
  integration: pr-video-agent-github
```

## 8. Wrap up

Summarize what you created: the profile, the integration, and the GitHub
App, and how it's set to run (repository, trigger, GIF or video, new
pushes). Then tell the user:

- **Change when it runs:** `npx omnara integrations update <integration-id>`
  with the whole `settings` object, or the **Pull requests** section on the
  integration's page in the dashboard.
- **Change how the app is run, the model, or anything else:** ask a coding
  agent with this skill. It updates the profile in place. Pull requests it
  hasn't worked on yet use the new config; one it has already recorded
  keeps the config it started with, so put a correction for that one in
  the mention.
- **`pr-video-agent.yaml`** is their copy of the config. They can commit it,
  keep it elsewhere, or delete it; the profile on Omnara is what runs.
- **Where the GIFs live:** on the ref `refs/uploads/pr-<number>` in the
  repository, which is not a branch and is never merged. Deleting that ref
  (`git push origin :refs/uploads/pr-<number>`) removes a pull request's
  recordings and breaks the images in its comments.
- **Remove it:** remove the integration (see "Removing" in `integrations.md`),
  then run `npx omnara profiles delete <agent-profile-id>`. Comments it
  already posted stay on the pull requests.
