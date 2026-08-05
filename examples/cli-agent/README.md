# cli-agent example

An interactive chat CLI built entirely on the Omnara TypeScript SDK
(`@omnara/sdk`). On startup it bootstraps everything the agent needs, then
drops into a chat loop:

1. Authenticates with a personal access token from `.env`.
2. Registers the current machine as a BYO machine (named after the local
   hostname), mints a machine daemon token, and runs `omnarad` for the session
   so agent processes can execute locally.
3. Upserts a project, a project machine grant, the model provider config /
   configured model / project model grant required by the profile, and an
   agent profile compiled from [agent-profile.yaml](agent-profile.yaml).
4. Launches an agent from the profile and streams its events.

## Setup

Requires Node.js 22+ and, for the default daemon command, a Go toolchain.

```sh
cd examples/cli-agent
pnpm install
cp .env.example .env   # set OMNARA_API_KEY and a provider API key
pnpm start
```

Rerun with `pnpm run start resume` to pick up a previous agent instead of
launching a new one; the CLI lists the project's active agents with the
profile each was launched from.

`OMNARA_API_KEY` accepts an `omnara_pat_` personal access token or an
`omnara_org_` org API key; org API keys are bound to one organization, so they
also require `OMNARA_ORG_ID`. With a personal access token and multiple
organizations, the CLI shows an arrow-key picker and remembers the choice.
The agent is told the CLI's working directory, and `run_command` starts there
via the injected machine source. The model in
`agent-profile.yaml` selects a provider config; when it does not exist yet the
CLI bootstraps it from a preset (`openai-prod`, `openrouter-prod`, or
`anthropic-prod`) using the matching `OPENAI_API_KEY` / `OPENROUTER_API_KEY` /
`ANTHROPIC_API_KEY` environment variable.

The daemon token, `daemon.json`, omnarad binary, log, and state live under
`~/.omnara-cli-agent` (override with `OMNARA_DAEMON_HOME`). `cli-state.json`
there persists the selected org, the `/model`, `/effort`, and `/permission`
settings (reapplied to new agents at startup), and the launched-agent history
used by `resume`. The CLI builds the
daemon from the repository with `go build ./cmd/daemon`; set `OMNARAD_BINARY`
to install a prebuilt `omnarad` into that home instead. No `omnarad install`
step is needed — the CLI writes the daemon configuration itself.

## Chat syntax

- `@notes.md` or `@[path with spaces.pdf]` attaches a file to the prompt.
- `$[skill-name]` asks the agent to use that skill for the request. The skill
  itself must be listed under `skills:` in `agent-profile.yaml` and granted to
  the project; the CLI only adds prompt text, it never invokes the skill.
- When the agent asks a question or requests permission, an arrow-key picker
  opens (space toggles multi-select options); options that accept text prompt
  for it after selection. In non-interactive terminals, type option numbers
  (`0`, `0+2` for multi-select) instead.
- While the agent works, a spinner above the input line shows its state
  (working, thinking with a live tail of the reasoning stream, writing,
  calling a tool, running tools). In `/display full` mode, completed
  reasoning blocks are also printed as dim `[thinking]` messages.
- `/quit` exits.

## Seeding models

`pnpm run seed-models` creates a batch of gpt-family configured models (with
reasoning enabled where the family supports it) under every OpenAI-format
model provider config in the org and grants them to every visible project.
It is idempotent: existing models are kept, and models missing
`supports_reasoning` are updated in place.

## Config commands

These update the running agent's config in place by compiling and submitting a
new config revision:

- `/model <model-slug>` switches the model, bootstrapping the configured model
  and project model grant under the profile's provider config when needed.
- `/effort <effort-level>` sets the model's reasoning effort (for example
  `low`, `medium`, or `high`; valid values depend on the provider).
- `/permission <tool> <ask|allow>` sets one declared tool's permission mode
  (`ask` = `always_ask`, `allow` = `always_allow`).
- `/permission <ask|allow>` sets the mode for every tool declared in the
  profile that supports it.
- `/display <simple|default|full>` switches the transcript detail level.
  `default` shows tool calls approval-style — the tool name with an
  abbreviated one-line summary (the command for `run_command`), and results
  with the tool name, outcome, and one abbreviated output line. `full` shows
  complete tool inputs and outputs with ids; `simple` shows only tool call
  name/id and the result outcome line. The choice persists in
  `cli-state.json`.

Tab completes command names, `/model` against the project's granted model
names, `/effort` against the known effort levels, `/permission` against the
declared tools, and `/display` against its modes.
