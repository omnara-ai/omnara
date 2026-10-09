# PR Review Agent

An agent that reviews your GitHub pull requests. It reads the change against
a fresh checkout of your repository, not just the diff, and publishes one
review with inline findings and one-click suggestions:

> **acme-reviewer** reviewed\
> Adds cursor-based pagination to `GET /invoices`. Two things to fix before merging; I couldn't run the integration tests, which need a database.
>
> **`api/invoices.ts` line 42:** `cursor` is decoded without validation, so a malformed value throws and returns a 500 instead of a 400.
>
> **`api/invoices.ts` line 58:** sorting only by `created_at` skips or repeats invoices created in the same millisecond across pages. Add `id` as a tiebreaker:
>
> ```suggestion
>   .orderBy([{ column: "created_at" }, { column: "id" }])
> ```

Reply to a comment to push back or ask why, or mention it to review again
after you push. It follows your repository's `AGENTS.md` and
`CONTRIBUTING.md`, focuses on bugs, security, breaking changes, and missing
tests, and skips the nits your linter already catches.

You choose when it runs: on every new pull request, only when mentioned, or
both (the default).

## What you need

- An Omnara account ([app.omnara.com](https://app.omnara.com)) with a machine
  pool; `default-pool` works as is
- A GitHub account or organization where you can register a GitHub App and
  install it on your repositories

No GitHub token: the deploy registers a GitHub App for you through Omnara.

## Deploy

Open Claude Code, Codex, Cursor, or any other coding agent and say:

```text
Read https://raw.githubusercontent.com/omnara-ai/omnara/main/examples/pr-review-agent/SKILL.md and follow it to deploy the PR review agent.
```

Your coding agent follows [SKILL.md](SKILL.md), which is plain steps with the
exact `npx omnara` commands if you'd rather run them yourself. Run it again
any time to change the agent; it updates it in place.

## How it works

[agent.yaml](agent.yaml) is the whole agent. Omnara's
[GitHub integration](https://docs.omnara.com/integrations/github) receives
your App's webhooks, and its launcher starts one agent per pull request, with
its own machine. The launcher also gives that agent the pull request tools
and Git credentials minted from the App's installation, so no token appears
in the config or the event log. The App gets read access to code and write
access to pull requests: it can comment and review, but it can't push, and
its reviews never approve or block a merge. Only people with write access can
start a review, by opening a pull request or mentioning the App. Later
comments, reviews, and pushes on that pull request reach the same agent,
which answers when it's addressed. What it looks for, the comment limit, and
how it handles new pushes are plain instruction text you can read and edit.
