# Browser Agent

A long-running agent that works in a real browser. It signs in to your web
apps with credentials you store in Omnara, then does the work there: pulling
numbers out of a dashboard, filling in forms, moving records between sites,
or working through a queue overnight. It can run one set job on a schedule,
or take whatever tasks your team sends it in Slack or Discord.

> Pull last month's invoices over $5,000 from the billing portal into a CSV.\
> Signed in. The invoice list paginates 25 at a time, so I'm filtering by date first. [screenshot]\
> Done: 38 invoices, $412,880 total. invoices-2026-09.csv is attached. Two were
> marked "disputed"; I left them in and flagged them in a column.

It sends screenshots with its updates, asks before anything it can't undo,
like payments, deletions, or sending messages, and asks you for CAPTCHAs and
texted codes instead of guessing.

## What you need

- An Omnara account ([app.omnara.com](https://app.omnara.com)) with a machine
  pool; `default-pool` works as is
- A sign-in for each web app, ideally accounts made for the agent

## Build it

Open Claude Code, Codex, Cursor, or any other coding agent and say:

```text
Read https://raw.githubusercontent.com/omnara-ai/omnara/main/examples/browser-agent/SKILL.md and follow it to build my browser agent.
```

Your coding agent follows [SKILL.md](SKILL.md). It first asks about the work,
which sites the agent signs in to and how, when it runs, whether you want it
in Slack or Discord, and what it may do without asking. Then it writes the agent from
your answers, stores the sign-ins, and has the agent sign in to each one while
you watch. SKILL.md is plain steps with the exact `npx omnara` commands if
you'd rather run them yourself.

## How it works

[agent.yaml](agent.yaml) is the whole agent: a template whose "Your job"
section and sign-ins come from the interview. The browser is Chrome on the
agent's own machine, driven by Vercel's
[agent-browser](https://github.com/vercel-labs/agent-browser) CLI through
`run_command`. The agent reads pages as accessibility snapshots, looks at
screenshots when layout matters, and attaches them to its updates.

**Sign-ins.** Each sign-in is a set of environment variables:
`LOGIN_<KEY>_URL` in the config, and `LOGIN_<KEY>_USERNAME`,
`LOGIN_<KEY>_PASSWORD`, and optionally `LOGIN_<KEY>_TOTP` from Omnara
secrets. The startup script saves each set in agent-browser's encrypted
vault, and the agent signs in with `agent-browser auth login <name>`, which
only fills the form on the sign-in URL's site. When a site splits the email
and password across pages, the agent fills the fields itself with the
variable (`fill @e6 "$LOGIN_CRM_PASSWORD"`), which the machine's shell
expands. Either way, passwords never pass through the model or show up in the
event log. The variables are visible to commands on the machine, and the
instruction tells the agent never to print them; use accounts with only the
access the job needs.

**Two-factor sign-in.** If a site uses an authenticator app, store the
account's setup key as `LOGIN_<KEY>_TOTP`, and the agent gets the current
code from `/workspace/bin/totp <name>`, so it can sign in unattended. That
puts both factors on the agent's machine, so use it with accounts made for
the agent. Codes by text or email need a person each time. All sign-ins share one browser
profile, so the agent stays signed in to several sites at once; two accounts
on the same site take turns.

**The machine.** The config favors reliability over cost:

| Setting | Default | In this agent |
| --- | --- | --- |
| `machine_memory_mb` | The pool's default size | `8192`, or the pool's maximum, for Chrome and Blaxel's in-memory file system |
| `delete_after_idle_minutes` | The pool's idle-deletion period, which would delete the machine and its sign-ins | `0`: kept until the agent is archived. In Slack every thread gets its own machine, so archive a thread's agent when its work is done, or set a period (at least `5` minutes) to remove idle machines automatically |
| `sleep_after_ms` | A short idle period on the default pool | `7200000`: sleeps after two hours with no command running, and wakes in under a second with Chrome, memory, and files intact. `0` (never) when a site texts or emails codes |
| `AGENT_BROWSER_IDLE_TIMEOUT_MS` | One hour: agent-browser closes the browser after an hour without commands | `0`: the browser stays open |

A working agent runs commands every few seconds, so it never sleeps mid-task.
A sleeping browser can't keep sessions alive, so the agent may sign in again
after waking; that's automatic unless the site texts or emails a code, which
is why those agents never sleep. Archive agents when their work is done:
kept machines count toward the pool's machine limit. A schedule sends every
run to one standing agent, so it uses one machine that stays signed in.

`sleep_after_ms` is a Blaxel, Unikraft Cloud, Freestyle, and Arker option. On
a Daytona, Modal, or Tenki pool, delete that line; those machines don't sleep.

The startup script pins agent-browser to a tested version, so a new release
can't change behavior mid-run. agent-browser runs Chrome under a background
process in its own session, so the browser outlives each `run_command` call.

**Long tasks.** The agent keeps a log at `/workspace/progress.md` of what it
has done and what's next, so it can pick up after a context compaction
without repeating actions that change data. Downloads land in
`/workspace/output`, and it delivers files from there.

## If a site blocks the agent

The browser runs on the agent's machine, from a datacenter IP, with no live
view. Most sites are fine with that. If one blocks it or keeps showing
CAPTCHAs, if someone needs to watch the browser live, or if a sign-in needs a
step the agent can't do, such as SSO with a phone prompt, switch to a hosted
browser such as [Kernel](https://www.kernel.sh) or
[Browserbase](https://www.browserbase.com). agent-browser supports both
through `AGENT_BROWSER_PROVIDER`, so the instruction and commands stay the
same: follow the provider's setup, store its API key as an Omnara secret, and
remove `AGENT_BROWSER_PROFILE`, since the provider keeps the profile.

Why not a browser MCP server? Omnara calls MCP servers from its control
plane, so signing in through one means the model types the password as a
tool argument, where it lands in the event log. A CLI on the machine keeps
credentials there and works with any browser backend.
