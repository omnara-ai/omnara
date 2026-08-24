# x-signal-agent example

A category-listening agent built on Omnara: once a day it searches X
(Twitter) for conversation about a topic you pick — by default managed
agents — filters aggressively, and delivers a digest of at most five items,
each with why it matters and a suggested action. One notebook cell holds the
topic and the X search query, so pointing it at your own category is a
two-line edit. The agent stays available after each digest: reply in the
Slack thread (or the console) to ask for reply drafts or push back on the
filtering. It never posts to X.

The whole example is [demo.ipynb](demo.ipynb), a Jupyter notebook on the
[Deno kernel](https://docs.deno.com/runtime/reference/cli/jupyter/) using the
Omnara TypeScript SDK (`@omnara/sdk`). The agent itself is one config object
in one cell — an instruction, a model, and its tools; there is no service
to deploy. The agent calls the X API with `curl` from a pool machine, with
the bearer token injected as a secret at runtime.

Cell by cell the notebook: resolves your org, project, and machine pool;
upserts the X bearer token as a project secret; sets the listening topic
and X search query; uploads the config as an
agent profile; launches a scan and follows its live event stream; and
(optionally) connects Slack and creates a daily
[cron trigger](https://docs.omnara.com/api-reference/endpoints/cron-triggers/create-cron-trigger).
The same config can also be expressed as YAML and pasted into the console —
JSON and YAML are two `source_format`s of the same agent config.

The notebook runs against your default org and project — new Omnara accounts
come with both, plus a managed machine pool already granted. The only console
prerequisite is the profile's model being granted to the project (new
accounts get that from onboarding too).

## X API access

You need an X developer account with pay-per-use billing enabled
([console.x.com](https://console.x.com)): create a project and an app, then
generate an app bearer token. The agent uses the
[recent search endpoint](https://docs.x.com/x-api/posts/search/introduction)
(last 7 days), reading at most 50 posts per run — post reads are billed
per request, so a daily scan costs on the order of cents.

## Setup

```sh
brew install deno         # or https://docs.deno.com/runtime/getting_started/installation/
deno jupyter --install    # register the Deno kernel with Jupyter

cd examples/x-signal-agent
cp .env.example .env      # set OMNARA_API_KEY and X_BEARER_TOKEN
```

There is nothing to `npm install`: [deno.json](deno.json) maps `@omnara/sdk`
to the published npm package, and Deno fetches it and its `zod` dependency.

Edit the `model` block in the notebook's agent cell to name a model provider
config and configured model from your organization — copy the exact names
from the console's **Models** page. Then open `demo.ipynb` (VS Code, Cursor,
or `jupyter lab`), pick the **Deno** kernel, and run the cells top to bottom.

Every cell is idempotent: rerun the notebook after editing the agent (for
example to tune the search query or the filtering rules) and it updates the
profile in place. Rerunning also rotates the secret to the current
`X_BEARER_TOKEN` value.

## Slack

The digest is most useful in a channel. Run the notebook's Slack cell with an
app configuration token from [api.slack.com/apps](https://api.slack.com/apps)
— or use **Deploy to app → Slack** on the profile's row in the console; the
[Slack integration](https://docs.omnara.com/integrations/slack) guide covers
the flow. Once installed, cron-triggered runs deliver the digest with
`send_integration_message`. Thread replies become agent inputs, so the team
can ask for a reply draft or question an item's inclusion right in the
thread. Without Slack, digests appear in the console (and in the notebook
when you launch from there).

## Schedule

The notebook's last cell creates a cron trigger named `x-signal-agent-daily`
(`0 9 * * 1-5` America/Los_Angeles — edit the cell to change it). Each firing
launches a fresh agent from the profile. Runs scan non-overlapping 24-hour
windows, so there is no dedupe state to store. Disable or delete the trigger
in the console to stop scheduled scans.

## Notes

- The instruction allows exactly one search request per run and caps the
  digest at five items. Both limits are deliberate: reads cost money, and an
  unbounded "here's everything from X today" digest trains the team to
  ignore it. An empty digest is a valid result.
- The X bearer token lives in a project secret and reaches the machine as
  the `X_BEARER_TOKEN` environment variable via `secret_env_overlay`; it
  never appears in the agent config or event log.
- The agent drafts replies but never posts to X. Posting would need
  user-context OAuth, per-post write billing, and more trust than a v1
  listening agent has earned.
