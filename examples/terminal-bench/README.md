# Terminal-Bench on Omnara

Runs [Terminal-Bench](https://www.tbench.ai) tasks with Omnara agents on ephemeral
[Blaxel](https://blaxel.ai) machines, then grades them the way
[Harbor](https://github.com/harbor-framework/harbor) does.

Each run creates one **agent profile** (named for the run). Every task in the
run gets its own **agent** under that profile, named `<run> · <task>`. Each
agent gets its own machine, booted from an image of that task's environment.

```
task.toml + environment/ ──bl push──▶ sandbox/tb21-<task>:<hash>  (Blaxel image, built once)
                                              │ machine_provider_options_overlay.image
agent profile "<run>" ──▶ agent "<run> · <task>" ──▶ pool machine (cwd = task WORKDIR)
                                              │ instruction.md as the first input
                                              ▼
                         verifier: 2.x in the same machine / 3.0+ in a separate Blaxel sandbox
```

## How a task runs

1. **Image.** The task's environment becomes a Blaxel sandbox image tagged by
   content hash, so it is only built once. 2.x tasks start from their published
   `docker_image`. 3.0+ tasks build their `environment/Dockerfile` on Blaxel's
   builders.

   A small layer is appended to every image. It keeps the task's `WORKDIR`
   and `USER`, and adds:
   - Blaxel's `sandbox-api` entrypoint.
   - `curl`, if the image lacks it, since Omnara's daemon bootstrap needs it.
   - The released `omnarad` binary, checked against its published digest, at
     `/usr/local/bin/omnarad`. The bootstrap installs it from there instead
     of downloading it at boot, which is slow from Blaxel's network.
2. **Agent.** The runner compiles [`agent.yaml`](agent.yaml) with the chosen
   model and a machine source. The machine source points at the
   `terminal-bench` pool with the task's image, memory and working directory.

   The runner launches the agent under the run's profile with no input yet.
   That launch provisions the task machine. Once the machine is online, the
   runner sends `instruction.md` as the first input and waits for the agent's
   final answer, up to the task's agent timeout.
3. **Verify.** Grading never happens before the agent finishes, and the agent
   never sees the tests.
   - **Shared verifier (2.x).** The runner uploads `tests/` to `/tests` on the
     agent's machine, runs `tests/test.sh` there and downloads
     `/logs/verifier/reward.txt`. It uses the machine exec and file APIs:
     `POST /machines/{id}/exec`, `PUT` and `GET /machines/{id}/files`.
   - **Separate verifier (3.0+).** The runner tars the task's declared
     `artifacts` on the machine and downloads them to `runs/`. It then grades
     them in a fresh Blaxel sandbox created directly with Blaxel's SDK, not
     through Omnara. The sandbox boots from `tests/Dockerfile` (tests baked
     in), built once per task as `sandbox/tb40v-<task>`. That image is never
     in the Omnara pool, so the agent can't reach it. The sandbox is deleted
     after grading.
4. **Results.** Results land in `runs/<run>/`: `summary.json` for the run, and
   for each task a `result.json`, the full `events.json` trajectory and the
   verifier logs.

## Setup

You need the Omnara CLI logged in, the Blaxel CLI (`bl`) logged in to the
workspace that holds the images. The Blaxel SDK uses that same login for
verifier sandboxes.

The run commands below assume the pool and models exist. Here is the one-time
setup, with the IDs used when this example was written:

```sh
# Blaxel pool. Images are chosen per task, so allow any image.
omnara pools create --name terminal-bench --provider blaxel \
  --provider-auth-secret-id <blaxel-api-key-secret> \
  --provider-config '{"workspace":"omnara-terminal-bench","allowed_images":["*"],"allowed_regions":["us-pdx-1"]}' \
  --default-machine-provider-options '{"image":"sandbox/tb-base:v1","region":"us-pdx-1"}' \
  --default-machine-memory-mb 4096 --max-machine-memory-mb 16384 \
  --max-total-memory-mb 98304 --max-total-machines 12 --delete-after-idle-minutes 30
omnara grant pools add --project <project> --machine-pool-id <pool-id>

# Models on the OpenRouter provider config, granted to the project.
omnara models create <provider-config-id> --name anthropic/claude-opus-5.5 \
  --provider-model-slug anthropic/claude-opus-5.5 --context-window-tokens 1000000 \
  --max-output-tokens 128000 --default-max-output-tokens 32000 --supports-tools --supports-reasoning
omnara grant models add --project <project> --configured-model-id <model-id>
```

The pool's default image lives in [`images/base`](images/base). Push it with
`bl push` from that directory.

## Run

```sh
pnpm install
pnpm tb --dataset terminal-bench@2.1 --tasks fix-git --model openai/gpt-6.1-sol
# Four runs at once (2 datasets × 2 models), sharing one machine budget:
pnpm tb --dataset terminal-bench@2.1,terminal-bench@4.0 \
  --model openai/gpt-6.1-sol,anthropic/claude-opus-5.5 --all-tasks --concurrency 48
```

Every dataset × model pair is a separate run with its own profile. Each task
image is built once and shared by every run that uses it. Runs are interleaved,
so they all progress together.

| Flag | Default | |
| --- | --- | --- |
| `--dataset` | `terminal-bench@2.1` | Comma-separated: `terminal-bench@2.0`, `@2.1`, `@4.0`. |
| `--model` | `anthropic/claude-opus-5.5` | Comma-separated configured model names. |
| `--tasks` / `--all-tasks` | | Comma-separated task names, or every task in each dataset. |
| `--exclude-categories` | `security` | Task categories to skip. Models often refuse these, so they would score as failures. |
| `--exclude-tags` | `security` | Task tags to skip. |
| `--provider-config` | `omnara-openrouter` | |
| `--reasoning-effort` | the model's default | |
| `--pool` | `terminal-bench` | Machine pool granted to the project. Its machine limit caps `--concurrency`. |
| `--blaxel-workspace` | `$BL_WORKSPACE` or `omnara-terminal-bench` | Where task images are pushed. |
| `--concurrency` | `16` | Agents (and machines) in flight across all runs. |
| `--build-concurrency` | `6` | Parallel `bl push` image builds. |
| `--verifier-concurrency` | `16` | Parallel Blaxel grading sandboxes (3.0+ tasks). |
| `--name` | `tb<version>` | Prefix for run and profile names. |
| `--resume` | | Comma-separated `runs/<run>` directories to continue (pass the same `--all-tasks`/`--tasks`). |
| `--retry-timeouts` | off | With `--resume`, rerun tasks whose agent hit its timeout. |

Once a task is graded and its trajectory saved, the runner archives that
task's agent. Archiving frees the agent's pool machine for the next task, and
the agent stays under its run's profile.

If the pool is full, a launch waits and retries. If the runner is interrupted,
`--resume runs/<run>` continues the run:
- Tasks that were already graded are kept.
- Agents that are still running are picked up where they are.
- Everything else is launched again.

`pnpm tb:cleanup <profile-id>...` archives every agent under the given profiles
and deletes the profiles.

Tasks that can't run on a single Blaxel machine are skipped and listed in each
run's `summary.json`. These are multi-container (docker-compose) tasks, GPU
tasks, and tasks that serve on port 8080, which Blaxel's `sandbox-api` already
occupies inside every machine.

Agent timeouts count from when the instruction is sent, including for agents a
resumed run picks up. The local Omnara worker runs only
`OMNARA_WORKER_CAPACITY` agent turns at once (default 4). Raise it, or run more
workers, before raising `--concurrency`. Otherwise tool calls queue and tasks
time out because of the harness, not the model.

The runner uses the login saved by `omnara login`. `OMNARA_API_KEY`,
`OMNARA_API_URL`, `OMNARA_ORG_ID` and `OMNARA_PROJECT_ID` override it.

## Caveats

- These are not leaderboard runs. Official submissions need Harbor's job
  output format, ATIF trajectories and 5 trials per task.
- The agent loop runs on Omnara, not inside the task container. Only the
  agent's shell and file tools touch the machine.
- 3.0+ graders run on Blaxel (amd64, like Harbor's). The runner doesn't
  enforce a task's `allow_internet = false` in the grading sandbox.
