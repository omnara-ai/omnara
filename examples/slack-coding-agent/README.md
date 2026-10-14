# Slack Coding Agent

A coding agent your team mentions in Slack, in Discord, or on a GitHub pull
request. Describe a bug, paste an error, or link an issue, and it works on a fresh clone of your repository, makes a
focused fix, and opens a pull request:

> **@Coding Agent** signups fail when the email has uppercase letters, can you fix it?\
> On it: reproducing with a test first, then tracing where emails get compared.\
> Found it: `findUserByEmail` compares raw input against lowercased emails in the database. Normalizing at the API boundary and adding a regression test.\
> Opened [#482: Normalize email case on signup and login](https://github.com/acme/app/pull/482). Tests pass; one existing test assumed case-sensitive lookups and is updated.

Reply in the thread to ask for changes and it pushes to the same pull
request. Mention it on a pull request and it reviews it or pushes fixes to
its branch. Larger tasks it splits across copies of itself working in parallel.
It follows your repository's `AGENTS.md`, and it keeps details from the
thread, like customer names, out of commits and pull requests.

## What you need

- An Omnara account ([app.omnara.com](https://app.omnara.com)) with a machine
  pool; `default-pool` works as is
- A Slack workspace or Discord server where you can install an app, or a
  GitHub account that can register a GitHub App for pull request mentions
- A GitHub token with write access to the repository (the deploy walks you
  through a fine-grained one)

## Deploy

Open Claude Code, Codex, Cursor, or any other coding agent and say:

```text
Read https://raw.githubusercontent.com/omnara-ai/omnara/main/examples/slack-coding-agent/SKILL.md and follow it to deploy the Slack coding agent.
```

Your coding agent follows [SKILL.md](SKILL.md), which is plain steps with the
exact `npx omnara` commands if you'd rather run them yourself. Run it again
any time to change the agent; it updates it in place.

## How it works

[agent.yaml](agent.yaml) is the whole agent, modeled on the coding agent
Omnara's team uses on its own repository. Each thread or pull request gets its own
agent and machine, where a startup script clones the repository with `gh`; the token
is injected as an environment variable and never appears in the config or the
event log. A `self` subagent lets it hand parts of a big task to copies of
itself, each in its own `git worktree` on the same machine. It works on
branches and opens pull requests rather than pushing to your default branch;
branch protection makes sure nothing merges without a review. Its PR rules,
privacy rules, and reply formatting are plain instruction text you can read and
edit. To let it read your issue tracker, add the tracker's MCP server (Linear,
Jira) in an `mcp` block.
