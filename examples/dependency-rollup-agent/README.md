# Dependency Roll-up Agent

An agent that turns your repository's stream of Dependabot pull requests into
one pull request a day that passes CI. It applies each open update, reads the
release notes, fixes the code an upgrade breaks, patches high and critical
security advisories, and tells you up front which upgrades it left out and
why:

> **build(deps): roll up dependency updates** · opened by **acme-dependencies**
>
> - **wait until the next roll-up for the linter 1.92.0**: 1.91.0 reports a
>   known false positive on our effect hooks, fixed upstream in 1.92.0. This
>   pull request keeps 1.90.0.
> - **skip TypeScript 7.0.2** (#412): it removes the compiler API our code
>   generation uses. Same decision as #371; nothing has changed.
>
> - #408, #409: AWS SDK config and S3 patches.
> - #410: Vite 8.3.6 with the plugin release it needs, in all four
>   workspaces.
> - Patched a high-severity advisory in a transitive dependency with a
>   lockfile-only update; no new overrides.
>
> Verified with the workspace tests and a frozen install; CI passes.

Review it like any pull request. Comment on it and the agent follows your
comments on its next run, if you have write access to the repository; merge
it and the agent starts the next one. It closes each Dependabot pull request
it rolled in, or decided against, with a one-line comment linking the
roll-up.

Omnara runs this agent on its own repository:
[omnara-ai/omnara#833](https://github.com/omnara-ai/omnara/pull/833) is a
pull request it opened.

## What you need

- An Omnara account ([app.omnara.com](https://app.omnara.com)) with a machine
  pool; `default-pool` works as is
- A GitHub repository with
  [Dependabot version updates](https://docs.github.com/en/code-security/dependabot/dependabot-version-updates/configuring-dependabot-version-updates)
  and CI on pull requests
- A GitHub account or organization where you can register a GitHub App and
  install it on that repository

No GitHub token: the deploy registers a GitHub App for you through Omnara.

## Deploy

Open Claude Code, Codex, Cursor, or any other coding agent and say:

```text
Read https://raw.githubusercontent.com/omnara-ai/omnara/main/examples/dependency-rollup-agent/SKILL.md and follow it to deploy the dependency roll-up agent.
```

Your coding agent follows [SKILL.md](SKILL.md), which is plain steps with the
exact `npx omnara` commands if you'd rather run them yourself. Run it again
any time to change the agent; it updates it in place.

## How it works

[agent.yaml](agent.yaml) is the whole agent. You launch it once, and a cron
trigger messages that same agent every morning, so its conversation holds
what it decided on earlier days. A hold it recommended last week still
stands today. The profile's
[`git_credentials`](https://docs.omnara.com/integrations/github#git-credentials)
authenticate git and `gh` on its machine as a GitHub App limited to one
repository, so no token appears in the config or the event log. Nobody
watches its conversation, so anything it's unsure about goes at the top of
the pull request instead of into a question.

On its first run it reads the Dependabot pull requests your team finished by
hand or closed without merging, and treats those as decisions. It follows
your repository's `AGENTS.md`, `CONTRIBUTING.md`, and `dependabot.yml`,
including `ignore` rules and any minimum release age.

It pushes only to its own branch and never merges. It takes direction only
from people with write access to the repository, and checks a commenter's
permission before following them; other comments, release notes, and the
packages themselves are information to it, not instructions. Its GitHub App
can't change files under `.github/workflows`, so Dependabot's GitHub Actions
updates stay open for a person. What it checks, how it writes the
description, and the branch name are plain instruction text you can read and
edit.
