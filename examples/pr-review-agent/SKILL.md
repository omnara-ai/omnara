---
name: pr-review-agent
description: Deploy or update the PR Review Agent on Omnara, an agent that reviews GitHub pull requests through Omnara's GitHub integration and leaves inline findings. Use when the user asks to deploy, set up, update, or remove the PR review agent.
---

# Deploy the PR Review Agent

You are setting up an Omnara agent for the user from `agent.yaml` in this
folder. The goal: a GitHub App on the user's repositories that reviews pull
requests the way they chose, and that the user has watched publish one
review.

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

No GitHub token is needed. The GitHub integration's launcher gives each agent
the pull request tools and Git credentials from the App's installation.

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

## 2. Choose how it works on GitHub

Ask the user these in one message, with the defaults, so they can answer
"defaults" or change only what they care about. Wait for the reply.

1. **When it reviews**, which becomes the launcher's `trigger`. Default
   `both`: it reviews every new pull request opened by someone with write
   access, and anyone with write access can mention it on any pull request.
   The alternatives are `pull_request_opened` (new pull requests only) and
   `mention` (only when someone writes, say, "@acme-reviewer review this").
   Each review runs its own agent and machine, so automatic reviews on a busy
   repository cost more than mentions. Pull requests from bots or people
   without write access never start one on their own.
2. **New pushes.** Default: it stays quiet when commits land on a pull
   request it reviewed, until someone asks it to review again. The
   alternative is reviewing each push automatically, covering only the new
   commits.
3. **Which repositories.** Default: the ones the user grants the App when
   installing it in step 5, so suggest granting only the repositories they
   want reviewed.
4. **Which GitHub App.** Default: a new App just for reviews, named after the
   team (for example, "Acme Reviewer"), so its comments are clearly the
   reviewer's. If `npx omnara integrations list --json` shows another
   `github_pr` integration, such as the coding agent's, mention that it can
   launch only one profile, so pointing it at this agent takes it away from
   the other one.

## 3. Pick the model and machine pool

1. `npx omnara grant models list --json`: each item has `model.provider_config`
   and `model.name`. Use `anthropic/claude-opus-5.5` on `omnara-openrouter` if
   it's granted (`agent.yaml` runs it at high reasoning effort), otherwise the
   strongest coding model on the list. The choice gives `MODEL_PROVIDER_CONFIG`
   and `MODEL_NAME`.
2. `npx omnara grant pools list --json`; each grant's `machine_pool.name` is a
   candidate `MACHINE_POOL`. Use `default-pool` if it's granted: its machines
   come with `git` and common language toolchains, so the agent can run tests
   to confirm a finding. Otherwise use the only pool, or ask if there are
   several. On a custom pool, the image needs `git` and an up-to-date
   `omnarad`, which supplies the Git credentials. If there are none, the agent
   can still review from the diff alone, which catches less: in step 4,
   delete the `machine_sources` block and the instruction's "Checking out the
   code" section, and tell the user.

## 4. Create the agent

1. Copy `agent.yaml` from this folder to `./pr-review-agent.yaml` in the
   current directory. If this folder isn't available locally, download
   https://raw.githubusercontent.com/omnara-ai/omnara/main/examples/pr-review-agent/agent.yaml
   instead.
2. In the copy, replace every placeholder:

   | Placeholder | Value |
   | --- | --- |
   | `{{MODEL_PROVIDER_CONFIG}}` | from step 3 |
   | `{{MODEL_NAME}}` | from step 3 |
   | `{{MACHINE_POOL}}` | from step 3 |

   Then confirm nothing is left: `grep -n '{{' pr-review-agent.yaml` must
   print nothing.
3. If the user wants every push reviewed, replace the instruction's paragraph
   that starts "When new commits are pushed" with:

   ```text
   When new commits are pushed, you're told the head changed. Review only
   what changed since your last review, as above, and publish it the same
   way. If the new commits don't change anything worth raising, post
   nothing.
   ```

   As written, it focuses on correctness, security, breaking changes, and
   missing tests, and leaves at most 10 inline comments. If the user named
   anything else to check or skip, edit the instruction's Reviewing section;
   team conventions are better kept in the repository's `AGENTS.md` or
   `CONTRIBUTING.md`, which it already follows.
4. Check for an existing profile: `npx omnara profiles list --name pr-review-agent --json`.
   - None: `npx omnara profiles create --name pr-review-agent --file ./pr-review-agent.yaml --json`
   - Exists: `npx omnara profiles update <agent-profile-id> --file ./pr-review-agent.yaml --json`

   If it rejects the reasoning effort, the chosen model doesn't support
   `high`: remove the `reasoning` block and retry. Note the profile `id`.

## 5. Connect GitHub

Set up the GitHub integration with the "GitHub pull requests" section of
[`integrations.md`](../integrations.md) (read it from
https://raw.githubusercontent.com/omnara-ai/omnara/main/examples/integrations.md
if you don't have the repo). For this agent:

- Name the integration `pr-review-agent-github`, and use the trigger from
  step 2 in its launcher, for example
  `{"launcher":{"trigger":"both","profile":"<agent-profile-id>"}}`. If the
  user chose to reuse another `github_pr` integration, update that one's
  launcher instead of creating a new one.
- Suggest an App name with the team's name, such as "Acme Reviewer"; its slug
  (`acme-reviewer`) is how the team mentions it. Grant the repositories from
  step 2.
- Skip the **Contents** write change. The App Omnara registers can already
  read code and post reviews, and a reviewer never pushes. Do have the user
  check the App's **Webhook URL**.
- If the App's installation covers more repositories than should be
  reviewed, add `"repository_id":"<id>"` to `launcher`, the repository's
  numeric ID as a string (`gh api repos/<owner>/<name> --jq .id` prints it).
  The launcher takes one repository.

## 6. Watch a first review

Pick the test from the trigger:

- With `mention` or `both`, have the user comment on an open pull request in
  one of the granted repositories: "@acme-reviewer review this", with their
  App's slug.
- With `pull_request_opened`, have the user open a new pull request, even a
  small one.

Omnara adds an 👀 reaction to a comment it accepted. The agent also shows up
in the Omnara console ([app.omnara.com](https://app.omnara.com)), where the
user can watch it read the diff and the code. A review takes a few minutes: the
machine starts, clones the repository, and the agent reads around each
change before it posts. If nothing happens after a couple of minutes, check
the App's webhook URL and its **Advanced** tab on GitHub, which lists recent
deliveries and their responses.

Once the review is up, suggest the user reply to one of its comments to see
it answer in the thread.

## 7. Wrap up

Summarize what you created: the profile, the integration, and the GitHub App,
and how it's set to run (trigger, new pushes, repositories). Then tell the
user:

- **Change when it runs:** `npx omnara integrations update <integration-id>`
  with the whole `settings` object, or the **Pull requests** section on the
  integration's page in the dashboard.
- **Change how it reviews, the model, or anything else:** ask a coding agent
  with this skill. It updates the profile in place, and the next review uses
  it.
- **Remove it:** remove the integration (see "Removing" in `integrations.md`),
  then run `npx omnara profiles delete <agent-profile-id>`. Reviews it already
  posted stay on the pull requests.
