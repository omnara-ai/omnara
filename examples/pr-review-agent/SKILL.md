---
name: pr-review-agent
description: Deploy or update the PR Review Agent on Omnara, an agent that reviews GitHub pull requests through Omnara's GitHub integration and leaves inline findings. Use when the user asks to deploy, set up, update, or remove the PR review agent.
---

# Deploy the PR Review Agent

You are setting up an Omnara agent for the user from `agent.yaml` in this
folder. The goal: a GitHub App on the user's repositories that reviews pull
requests the way they chose, and that the user has watched publish one
review.

The steps below are the usual path and have the exact commands. Skip anything
that's already done (for example, the profile and integration exist and the
user only wants a different trigger), follow the user's lead if they want a
different order, and narrate briefly as you go.

- Start from `agent.yaml` as written. If the user wants something different
  (the instruction, tools, or model), change it and tell them what you
  changed.
- If a command fails, read the error and fix it; `npx omnara <command> --help`
  and [docs.omnara.com](https://docs.omnara.com) have the details. If you're
  stuck, show the user the error.

The commands use the Omnara CLI (`npx omnara`) because its login handles auth
in one step; add `--json` when you need to read IDs from the output. They're a
reference, not a requirement: the Omnara MCP tools, the
[REST API](https://docs.omnara.com/api-reference/openapi.yaml), or the SDK work
too, and the flags map directly to API fields. What matters is the result: a
profile from the filled-in `agent.yaml` and a GitHub integration that launches
it. Registering the GitHub App needs the Omnara dashboard.

No GitHub token is needed. The integration's launcher gives each agent the
pull request tools and Git credentials from the App's installation.

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

## 2. Choose how it works on GitHub

Ask these together in one message, with the defaults stated, so the user can
answer "defaults" or change only what they care about. Wait for the answer.

1. **When it reviews.** This becomes the launcher's `trigger`.
   - `both` (default): it reviews every new pull request opened by someone
     with write access, and anyone with write access can mention it on any
     pull request to review it or ask about it.
   - `pull_request_opened`: only new pull requests, automatically.
   - `mention`: only when someone mentions it, for example
     "@acme-reviewer review this".

   Each review runs its own agent and machine, so a busy repository with
   `both` or `pull_request_opened` uses more than `mention`. Pull requests from
   bots or from people without write access never start a review on their own;
   with mentions on, a teammate can mention the App on them.
2. **New pushes.** By default, it stays quiet when commits are pushed to a
   pull request it reviewed, until someone asks it to review again. The
   alternative is to review each push automatically (only the new commits).
3. **Which repositories.** By default, the ones the user grants to the App
   when they install it in step 6; suggest granting only the repositories
   they want reviewed. If the App's installation already covers more (for
   example, reusing an App), the launcher can also be limited to a single
   repository.
4. **Which GitHub App.** By default, a new App just for reviews, named after
   the team (for example, "Acme Reviewer"), so its comments are clearly the
   reviewer's. Check `npx omnara integrations list --json` for other
   `github_pr` integrations: one from another agent, such as the coding
   agent's, launches exactly one profile, so pointing it at this agent would
   take it away from the other one. Recommend a new App unless the user wants
   that.

## 3. Tune the review (optional)

As written, it focuses on correctness, security, breaking changes, and
missing tests, skips style a linter would catch, and leaves at most 10 inline
comments per review. It already follows the repository's `AGENTS.md`,
`CONTRIBUTING.md`, and any review guidelines it finds, so team conventions
are best kept there. Ask whether the user wants anything different, such as
a stricter or lighter touch, a different comment limit, or areas to always
check (migrations, public API changes, accessibility). If so, edit the
instruction's Reviewing section in your copy of `agent.yaml` in step 5.
Otherwise keep the default.

## 4. Pick the model and machine pool

1. `npx omnara grant models list --json`: each item has
   `model.provider_config` and `model.name`. Ask the user which model to use.
   Suggest `anthropic/claude-opus-5.5` on `omnara-openrouter` if it's granted
   (`agent.yaml` runs it at high reasoning effort); otherwise suggest the
   strongest coding model on the list. Name a couple of alternatives rather
   than the whole list, and show everything if they ask. The choice gives
   `MODEL_PROVIDER_CONFIG` and `MODEL_NAME`.
2. `npx omnara grant pools list --json`; each grant's `machine_pool.name` is a
   candidate `MACHINE_POOL`. Suggest `default-pool` if it's granted: its
   machines come with `git` and common language toolchains, so it can run
   tests to confirm a finding. Otherwise use the only pool, or ask if there
   are several. On a custom pool, the image needs `git` and an up-to-date
   `omnarad`, which supplies the Git credentials. If there are none, the agent
   can still review from the diff alone, which catches less: in step 5,
   delete the `machine_sources` block and the instruction's "Checking out the
   code" section, and tell the user.

## 5. Create the agent

1. Copy `agent.yaml` from this folder to `./pr-review-agent.yaml` in the
   current directory. If this folder isn't available locally, download
   https://raw.githubusercontent.com/omnara-ai/omnara/main/examples/pr-review-agent/agent.yaml
   instead.
2. In the copy, replace every placeholder:

   | Placeholder | Value |
   | --- | --- |
   | `{{MODEL_PROVIDER_CONFIG}}` | from step 4 |
   | `{{MODEL_NAME}}` | from step 4 |
   | `{{MACHINE_POOL}}` | from step 4 |

   Then confirm nothing is left: `grep -n '{{' pr-review-agent.yaml` must
   print nothing.
3. Apply the choices from steps 2 and 3. If the user wants every push
   reviewed, replace the instruction's paragraph that starts "When new
   commits are pushed" with:

   ```text
   When new commits are pushed, you're told the head changed. Review only
   what changed since your last review, as above, and publish it the same
   way. If the new commits don't change anything worth raising, post
   nothing.
   ```

4. Check for an existing profile: `npx omnara profiles list --name pr-review-agent --json`.
   - None: `npx omnara profiles create --name pr-review-agent --file ./pr-review-agent.yaml --json`
   - Exists: `npx omnara profiles update <agent-profile-id> --file ./pr-review-agent.yaml --json`

   If it rejects the reasoning effort, the chosen model doesn't support
   `high`: remove the `reasoning` block and retry. Note the profile `id`.

## 6. Connect GitHub

If the user chose to reuse another `github_pr` integration in step 2, skip to
step 1's `update` command and set this profile and the trigger on it; it
keeps its App and installation.

If an integration named `pr-review-agent-github` already exists from an
earlier run of this skill, reuse it whatever its state, since names are
unique in a project. If `npx omnara integrations get <integration-id> --json`
doesn't show this profile and the chosen trigger under `settings.launcher`,
set them with the `update` command below. Then skip to step 3 if it's
`active`, since Omnara can't tell whether that was done on GitHub, or to
step 2 if it's `disconnected`; the integration's page shows what's left.

1. Create the integration with this profile and the trigger from step 2 in
   its launcher, and note the `itg_…` ID it returns:

   ```sh
   npx omnara integrations create --name pr-review-agent-github \
     --integration-kind github_pr \
     --settings '{"launcher":{"trigger":"both","profile":"<agent-profile-id>"}}' --json
   ```

   The name is permanent and names the agent's pull request tools
   (`int__pr-review-agent-github__read`). To change the launcher later, run
   `npx omnara integrations update <integration-id> --settings '...'` with the
   whole `settings` object. To limit it to one repository, add
   `"repository_id":"<id>"` to `launcher`, where the ID is the repository's
   numeric ID as a string (`gh api repos/<owner>/<name> --jq .id` prints it,
   or ask the user).
2. Registering the GitHub App happens in the browser. Have the user open
   `https://app.omnara.com/projects/<project-id>/integrations/<integration-id>`,
   pick the account that owns the repositories, and select **Continue to
   GitHub**. On GitHub they name the App. Names are unique across GitHub, so
   suggest one with the team's name, such as "Acme Reviewer"; its slug
   (`acme-reviewer`) is how the team mentions it. Back in Omnara they select
   **Choose repositories on GitHub**, grant the repositories from step 2, and
   then **Connect integration**. An organization's owners may need to approve
   the installation first.

   Omnara registers the App with read access to code and write access to
   pull requests, which is all a reviewer needs; there's no permission to
   change on GitHub.
3. On the App's **General** page on GitHub (**Settings** → **Developer
   settings** → **GitHub Apps** → the App → **Edit**, under the
   organization's settings if an organization owns it), have the user check
   that **Webhook URL** is
   `https://app.omnara.com/api/integrations/github/events`, and correct it if
   not; pull requests and mentions don't reach Omnara otherwise.

## 7. Watch a first review

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
the webhook URL from step 6 and the App's **Advanced** tab on GitHub, which
lists recent deliveries and their responses.

Once the review is up, suggest the user reply to one of its comments to see
it answer in the thread.

## 8. Wrap up

Summarize what you created: the profile, the integration, and the GitHub App,
and how it's set to run (trigger, new pushes, repositories). Then tell the
user:

- **Change when it runs:** `npx omnara integrations update <integration-id>`
  with new `settings`, or the **Pull requests** section on the integration's
  page in the dashboard.
- **Change how it reviews, the model, or anything else:** ask a coding agent
  with this skill. It updates the profile in place, and the next review uses
  it.
- **Remove it:** `npx omnara integrations delete <integration-id>`, then
  `npx omnara profiles delete <agent-profile-id>`. Delete the GitHub App in
  GitHub's settings as well; reviews it already posted stay on the pull
  requests.
