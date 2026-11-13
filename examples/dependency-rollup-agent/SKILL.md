---
name: dependency-rollup-agent
description: Deploy or update the Dependency Roll-up Agent on Omnara, a long-running agent that rolls a GitHub repository's Dependabot pull requests into one pull request that passes CI, on a daily schedule. Use when the user asks to deploy, set up, update, or remove the dependency roll-up agent.
---

# Deploy the Dependency Roll-up Agent

You are setting up an Omnara agent for the user from `agent.yaml` in this
folder. The goal: one long-running agent that keeps a single dependency pull
request open on the user's repository, that the user has watched work once,
and that a cron trigger messages every day.

The steps below are the usual path, with the exact commands. Skip anything
that's already done (for example, the agent is running and the user only
wants a different schedule), and follow the user's lead if they want a
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

No GitHub token is needed. The agent's git and `gh` are authenticated from a
GitHub App the user registers through Omnara in step 4.

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

## 2. Choose the repository and the schedule

Ask the user these in one message, with the defaults, so they can answer
briefly. Wait for the reply.

1. **Which repository** it keeps current (`GITHUB_REPO`, as `owner/name`).
   Default: the repository you're working in, if it's on GitHub. One agent
   keeps one repository; for another repository, run this skill again and
   add the repository's name to the profile, agent, and trigger names.
2. **When it runs.** Default: every day at 6am in their timezone. If the
   repository's `.github/dependabot.yml` sets a `time`, suggest an hour
   after it, so the day's Dependabot pull requests are open when the agent
   starts.
3. **Anything it can't learn from the repository**, such as an upgrade the
   team is holding back or something CI never builds. The agent already
   reads the repository's past Dependabot pull requests, `AGENTS.md`,
   `CONTRIBUTING.md`, and `dependabot.yml`, so most teams have nothing to
   add.

Then check the repository:

- Its numeric ID is `GITHUB_REPO_ID`: `gh api repos/<owner>/<name> --jq .id`.
  Without `gh`, `curl -s https://api.github.com/repos/<owner>/<name>` returns
  it as `id` for a public repository; otherwise ask the user to run the `gh`
  command.
- It should have a `.github/dependabot.yml` with version updates. Without
  one there are no Dependabot pull requests to roll up, and the agent only
  patches security advisories. Offer to add one with
  [GitHub's guide](https://docs.github.com/en/code-security/dependabot/dependabot-version-updates/configuring-dependabot-version-updates);
  that's a change to the user's repository, so open it as a pull request.

## 3. Pick the model and machine pool

1. `npx omnara grant models list --json`: each item has `model.provider_config`
   and `model.name`. Use `openai/gpt-6.1-sol` on `omnara-openrouter` if it's
   granted (`agent.yaml` runs it at `xhigh` reasoning effort), otherwise the
   strongest coding model on the list. The choice gives
   `MODEL_PROVIDER_CONFIG` and `MODEL_NAME`.
2. `npx omnara grant pools list --json`; each grant's `machine_pool.name` is a
   candidate `MACHINE_POOL`. Use `default-pool` if it's granted: its machines
   come with `git`, `gh`, common language toolchains, PostgreSQL, and Redis.
   Otherwise use the only pool, or ask if there are several. If there are
   none, tell the user the project needs a machine pool; the agent can't work
   on code without one. On a custom pool, the image needs `git`, `gh`, an
   up-to-date `omnarad` (it supplies the Git credentials), and whatever the
   repository's tests need; in step 5, rewrite the instruction's bullet that
   starts "Your machine keeps its files" to say what the image has.

## 4. Connect GitHub

The agent pushes a branch, opens a pull request, and comments on and closes
Dependabot's pull requests as a GitHub App. Nothing launches from GitHub, so
the integration has no launcher; the agent's profile refers to it by name.

1. Check `npx omnara integrations list --json` for a `github_pr` integration
   named `dependency-rollup-agent-github` and reuse it whatever its state. If
   the user would rather reuse another agent's GitHub App, such as the coding
   agent's, that works when its integration has `"code_write": true` in
   `settings` and its App is installed on the repository; commits and pull
   requests then appear under that App's name. Otherwise create one:

   ```sh
   npx omnara integrations create --name dependency-rollup-agent-github \
     --integration-kind github_pr --settings '{"code_write":true}' --json
   ```

   `"code_write": true` is what lets the agent push. To add it to an
   existing integration, pass its whole current `settings` object with
   `code_write` added to `npx omnara integrations update <integration-id>`,
   since an update replaces `settings`.
2. Registering the App needs the browser. The user opens
   `https://app.omnara.com/projects/<project-id>/integrations/<integration-id>`,
   picks the account that owns the repository, selects **Continue to
   GitHub**, and names the App. Names are unique across GitHub, so suggest
   one with the team's name, such as "Acme Dependencies". Back in Omnara,
   they select **Choose repositories on GitHub**, grant only the repository
   from step 2, and select **Connect integration**. An organization's owners
   may need to approve the installation. Skip the **Pull requests** section
   of the integration's page; this agent isn't started from GitHub.
3. `npx omnara integrations get <integration-id> --json` should show
   `"state": "active"`. Its `name` is `GITHUB_INTEGRATION`, and its
   `bot_mention` without the `@` is `GITHUB_APP_SLUG`.

If the repository has rules that restrict who can create branches or open
pull requests, the user allows the App there. The agent never pushes to the
default branch and never merges.

## 5. Create the agent profile

1. Copy `agent.yaml` from this folder to `./dependency-rollup-agent.yaml` in
   the current directory. If this folder isn't available locally, download
   https://raw.githubusercontent.com/omnara-ai/omnara/main/examples/dependency-rollup-agent/agent.yaml
   instead.
2. In the copy, replace every placeholder:

   | Placeholder | Value |
   | --- | --- |
   | `{{GITHUB_REPO}}` | from step 2 (it appears twice in the instruction) |
   | `{{GITHUB_REPO_ID}}` | from step 2, as a bare number |
   | `{{GITHUB_INTEGRATION}}` | from step 4 |
   | `{{GITHUB_APP_SLUG}}` | from step 4 (it appears several times) |
   | `{{MODEL_PROVIDER_CONFIG}}` | from step 3 |
   | `{{MODEL_NAME}}` | from step 3 |
   | `{{MACHINE_POOL}}` | from step 3 |

   Then confirm nothing is left: `grep -n '{{' dependency-rollup-agent.yaml`
   must print nothing.
3. If the user named anything in step 2's third question, add each as a
   bullet under the instruction's "What you can't tell from the repository"
   heading, in their words, with the reason. For example:

   ```text
   - Keep the editor package at 0.52 until the worker migration is done;
     newer releases load workers differently.
   ```

4. Check for an existing profile: `npx omnara profiles list --name dependency-rollup-agent --json`.
   - None: `npx omnara profiles create --name dependency-rollup-agent --file ./dependency-rollup-agent.yaml --json`
   - Exists: `npx omnara profiles update <agent-profile-id> --file ./dependency-rollup-agent.yaml --json`

   If it rejects the reasoning effort, the chosen model doesn't support
   `xhigh`: use `high`, or remove the `reasoning` block, and retry. If it
   rejects `machine_memory_mb`, lower it to the pool's limit or remove the
   line. Note the profile `id`.

## 6. Start the agent and watch its first run

This agent is launched once and kept: its conversation is its record of what
it decided, and its machine keeps the clone. Check for one from an earlier
setup with
`npx omnara agents list --agent-profile-id <agent-profile-id> --json`. If
there is one, replace its config instead of launching another, then ask it
for a run:

```sh
npx omnara agents update <agent-id> --file ./dependency-rollup-agent.yaml --json &&
npx omnara agents input <agent-id> --message "Run the dependency roll-up for today." --json
```

Otherwise launch it:

```sh
npx omnara agents launch --profile <agent-profile-id> --name dependency-rollup-agent \
  --message "Run the dependency roll-up for today." --json
```

Note the agent `id`, and give the user the link to watch it work:
`https://app.omnara.com/projects/<project-id>/agents/<agent-id>`. The first
run is the longest: it clones the repository, reads how past Dependabot pull
requests went, applies the open updates, and waits for CI. Don't wait for it
to finish; go on to step 7. When it's done it replies with the pull
request's link, or one line saying there was nothing to do.

If `git push` is denied, the App is missing **Contents: Read & write** or
the integration lacks `code_write`; see step 4.

## 7. Schedule it

Create a cron trigger that messages this agent, with `--cron` and
`--timezone` from step 2:

```sh
npx omnara crons create --name dependency-rollup-agent-daily \
  --target-type agent --target-agent-id <agent-id> --target-delivery-mode queued \
  --cron '0 6 * * *' --timezone America/Los_Angeles \
  --message-template 'Run the daily dependency roll-up for {{.trigger.local_date}}.' --json
```

`{{.trigger.local_date}}` is the trigger's own template variable, not a
placeholder to fill in. `queued` means a message that arrives while the agent
is still working waits for it to finish. If
`npx omnara crons list --name dependency-rollup-agent-daily --json` already
shows a trigger, change it with `npx omnara crons update <cron-trigger-id>`
(same flags) instead of creating a second one.

The trigger targets the agent, not the profile, on purpose: a profile target
would start a fresh agent every day that has forgotten yesterday's decisions.

## 8. Wrap up

Summarize what you created: the GitHub App and its integration, the profile,
the agent, and the cron trigger with its schedule. Then tell the user:

- **Steer it from the pull request.** Review comments on its pull request
  override its instruction, and it reads them on its next run. To have it act
  right away, message the agent in the Omnara console.
- **It never merges.** A person reviews and merges the roll-up, and the
  agent starts the next one from the default branch. Dependabot's GitHub
  Actions updates change workflow files it can't push, so those stay open
  for a person.
- **Change how it works, the model, or the schedule:** ask a coding agent
  with this skill. It updates the profile and the running agent in place.
- **Remove it:** delete the cron trigger (`npx omnara crons delete <id>`),
  archive the agent (`npx omnara agents archive <agent-id>`), then run
  `npx omnara profiles delete <agent-profile-id>` and remove the integration
  (see "Removing" in
  [`integrations.md`](../integrations.md)). Close its open pull request on
  GitHub if there is one. Dependabot doesn't reopen an update that was
  closed until a newer version comes out, so comment `@dependabot reopen` on
  any the agent closed that the user still wants.
