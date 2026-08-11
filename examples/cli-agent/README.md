# cli-agent example

An interactive chat CLI built entirely on the Omnara TypeScript SDK
(`@omnara/sdk`). On startup it bootstraps everything the agent needs, then
drops into a chat loop:

1. Authenticates with a personal access token from `.env` and selects an
   organization (it never creates one).
2. Asks where the agent should run commands: a machine from an org machine
   pool, the local machine (registered as a BYO machine named after the local
   hostname, with `omnarad` run for the session), or an existing machine
   whose daemon is already running elsewhere. Pass `--cloud` or `--local` to
   skip the prompt and pick the machine pool or local machine directly.
3. Upserts the `cli-agent` project, the machine or machine-pool grant for the
   selection, project model grants for every configured model in the org, and
   an agent profile compiled from [agent-profile.yaml](agent-profile.yaml).
4. Launches an agent from the profile and streams its events.

## Setup

Requires Node.js 22+. The local machine option also needs a prebuilt
`omnarad` binary via `OMNARAD_BINARY` (e.g. `go build -o omnarad ./cmd/daemon`
from the repository root).

```sh
cd examples/cli-agent
pnpm install
cp .env.example .env   # set OMNARA_API_KEY
pnpm start
```

Rerun with `pnpm run start resume` to pick up a previous agent instead of
launching a new one; the CLI lists the project's active agents. Resume skips
the machine question — the agent's machine sources are already part of its
config — and replays recent history (up to the last 500 events) from the
events endpoint before attaching to the live stream. `--cloud` and `--local`
only apply when starting a new agent.

`OMNARA_API_KEY` takes an `omnara_pat_v1_` personal access token, which must
already belong to at least one organization. With multiple organizations, the
CLI shows an arrow-key picker (or set `OMNARA_ORG_ID`). With the local
machine option, the agent is told the CLI's working directory and
`run_command` starts there via the injected machine source. The model in
`agent-profile.yaml` selects a
provider config and configured model that must already exist in the org (the
cluster-managed OpenRouter provider sets these up automatically); the CLI
grants all configured models to the project at startup.

With the machine pool option, set `REPO_URI` to clone a repository onto the
machine: the CLI injects a `startup_script` provider option that runs at
machine boot, before the daemon starts, and points `run_command`'s `cwd` at
the clone (`/workspace/repo`). Set `REPO_CRED` to a GitHub personal access
token to clone a private repository — the token is upserted as a
project-level secret and wired into the machine-pool grant's
`default_machine_secret_env_overlay`, so it is part of the machine
environment at provisioning (which the startup script runs with) and never
appears in the agent config. Both are ignored for the local and
machine-daemon options.

When the local machine option is used, the daemon token, `daemon.json`, the
installed omnarad binary, and its log live under `~/.omnara-cli-agent`
(override with `OMNARA_DAEMON_HOME`). No `omnarad install` step is needed —
the CLI writes the daemon configuration itself.

## Chat syntax

- `$[skill-name]` asks the agent to use that skill for the request. The skill
  itself must be listed under `skills:` in `agent-profile.yaml` and granted to
  the project; the CLI only adds prompt text, it never invokes the skill.
- When the agent asks a question or requests permission, an arrow-key picker
  opens (space toggles multi-select options); options that accept text prompt
  for it after selection. In non-interactive terminals, type option numbers
  (`0`, `0+2` for multi-select) instead.
- While the agent works, a spinner above the input line shows its state
  (working, thinking with a live tail of the reasoning stream, writing,
  calling a tool, running tools).
- Tool calls in progress show on a live line above the input with their name
  and input summary, then print as a single permanent entry once the result
  arrives: the tool name, colored outcome, the abbreviated input summary
  (the command for `run_command`), and one abbreviated output line.
- `/quit` exits.

## Config commands

These update the running agent's config in place by compiling and submitting a
new config revision:

- `/model <model-slug>` switches the model to another granted model.
- `/effort <effort-level>` sets the model's reasoning effort (for example
  `low`, `medium`, or `high`; valid values depend on the provider).
- `/permission <tool> <ask|allow>` sets one declared tool's permission mode
  (`ask` = `always_ask`, `allow` = `always_allow`).
- `/permission <ask|allow>` sets the mode for every tool declared in the
  profile that supports it.
- `/mode <queue|steer>` picks how prompts are delivered while the agent is
  working: `queue` (the default) queues them for the next turn, `steer`
  interrupts the current turn — the input is delivered as a steering input
  and open tool interactions are canceled.

Tab completes command names, `/model` against the project's granted model
names, `/effort` against the known effort levels, `/permission` against the
declared tools, and `/mode` against `queue` and `steer`.
