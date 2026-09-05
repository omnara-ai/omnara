// ---
// jupyter:
//   jupytext:
//     text_representation:
//       extension: .ts
//       format_name: percent
//   kernelspec:
//     display_name: Deno
//     language: typescript
//     name: deno
// ---


// %% [markdown]
// # PostHog Pulse Agent — live demo
//
// An agent that reads your PostHog for you. Every morning it queries your
// project through [PostHog's hosted MCP server](https://posthog.com/docs/model-context-protocol),
// compares yesterday against the recent trend, and delivers a short **usage
// pulse**: active users, event volume, top events, and callouts for anything
// that moved. The whole agent is one config object; there is no service to
// deploy — and no machine either, since its only tools are MCP calls and
// Slack delivery.
//
// Before you run it:
//
// 1. Get an Omnara personal access token ([app.omnara.com](https://app.omnara.com)).
// 2. Create a PostHog personal API key with the
//    [MCP Server preset](https://app.posthog.com/settings/user-api-keys?preset=mcp_server).
//    The preset scopes the key to one PostHog project and routes it to the
//    right region automatically.
// 3. `cp .env.example .env` and fill in `OMNARA_API_KEY` and `POSTHOG_API_KEY`.
// 4. Fetch [`@omnara/sdk`](https://www.npmjs.com/package/@omnara/sdk) with
//    your runtime's installer — once, in this folder: `deno install`,
//    `npm install`, or `bun install`.
//
// Then run it with any TypeScript runtime — nothing in this file is
// runtime-specific, and `.env` loading is native everywhere:
//
// - [Deno](https://docs.deno.com/runtime/): `deno run --env-file --allow-all demo.ts`
// - Node 22.18+: `node --env-file=.env demo.ts`
// - [Bun](https://bun.sh): `bun demo.ts` (reads `.env` automatically)
//
// Prefer notebook cells? This file is jupytext percent format — open it in
// Jupyter with the Deno kernel (`deno jupyter --install`).

// %%
import { bearerToken, createOmnaraClient, openAgentEventStream, sdk } from '@omnara/sdk'

// process.env is available in Deno, Node, and Bun; declaring it inline keeps
// this file dependency-free (no @types/node).
declare const process: { env: Record<string, string | undefined> }
const env = process.env
if (!env.OMNARA_API_KEY) throw new Error('set OMNARA_API_KEY in .env')

const client = createOmnaraClient({
  baseUrl: 'https://api.omnara.com/v1',
  auth: bearerToken(env.OMNARA_API_KEY),
})

// %% [markdown]
// ## 1. Where it lives
//
// Every account has a default org and a default project; the agent lives in
// the first of each. It needs no machine: its only tools are the PostHog MCP
// server (hosted by PostHog) and Slack delivery, so there is nothing to
// provision.

// %%
const { data: me } = await sdk.getCurrentUser({ client })
const org = me.orgs[0]
const { data: projects } = await sdk.listVisibleProjects({ client, path: { orgID: org.id } })
const project = projects.data[0]
const path = { orgID: org.id, projectID: project.id }

console.log('org:    ', org.name, org.id)
console.log('project:', project.name, project.id)

// %% [markdown]
// ## 2. The PostHog key becomes a secret
//
// The API key becomes a project secret, and the agent config references only
// its `sec_…` ID from the MCP server's `auth` block. Omnara attaches the
// value as the `Authorization: Bearer` header on every MCP call; it never
// appears in the config or the event log. Rerunning this section rotates the
// secret to the current `.env` value.

// %%
if (!env.POSTHOG_API_KEY) throw new Error('set POSTHOG_API_KEY in .env')
const secretName = 'posthog-pulse-agent-api-key'
const material = { kind: 'generic' as const, value: env.POSTHOG_API_KEY }

const { data: secrets } = await sdk.listSecrets({
  client,
  path: { orgID: org.id },
  query: { owner_kind: 'project', owner_project_id: project.id, name: secretName },
})
const existingSecret = secrets.data.find((secret) => secret.name === secretName)

let secretId: string
if (existingSecret) {
  await sdk.createSecretVersion({
    client,
    path: { orgID: org.id, secretID: existingSecret.id },
    body: { material },
  })
  secretId = existingSecret.id
} else {
  const { data: secret } = await sdk.createSecret({
    client,
    path: { orgID: org.id },
    body: { owner: { kind: 'project', project_id: project.id }, name: secretName, material },
  })
  secretId = secret.id
}
console.log(existingSecret ? 'secret updated:' : 'secret created:', secretId)

// %% [markdown]
// ## 3. The agent — this object is the whole thing
//
// One object holds everything: the instruction, the model, one MCP server
// (PostHog's, authenticated with the secret above), and two delivery tools.
//
// The MCP URL's `features` filter narrows PostHog's tool catalog to the
// read-only query surface, so as far as this agent is concerned no write
// tools exist. What to gather, the call budget, and the report format are
// plain English in the instruction — edit them and rerun this section. Set
// `model` to names from your console's **Models** page.

// %%
const agent = {
  // the profile name is set below in createAgentProfile; the config itself has no name field
  instruction: `
You are a product-metrics reporting agent for a team using PostHog. Each
run: query PostHog for the last full day of usage, compare it against the
recent trend, and deliver a short daily pulse.

Fetch. Use the PostHog MCP tools — they are already connected to the
team's PostHog project. Gather exactly two things every run:

1. The daily trend over the last 8 full days: unique active users and
   total event volume per day. A trends query with a unique-users series
   and a total-count series covers this in one call.

2. Yesterday's top 10 events by volume, with unique-user counts — a
   trends query for yesterday broken down by event name, or SQL if the
   breakdown is awkward.

If you are unsure which events exist, use the MCP's schema and discovery
tools before querying. You may make up to 3 further queries to
investigate anything anomalous, but stay under 8 MCP calls per run. If a
call fails, report the error and stop; do not retry in a loop.

Report. A short pulse, not a data dump:
- Headline: yesterday's active users and total events, each with the delta
  vs the trailing 7-day average (e.g. "412 active users, +9% vs 7-day avg").
- Top events: the 5 biggest by volume, with user counts.
- Callouts: anything that moved more than ~30% vs the 7-day average, with
  your best one-line read on why (a new event name appearing, an event
  going silent, a spike concentrated in one event). If nothing moved, say
  "steady day" — never invent a story, and never pad the report.

Deliver. When this conversation is driven through an integration such as
Slack, send the pulse with send_integration_message — the external user
only sees messages sent that way. Otherwise present it directly in the
conversation. If someone replies with a follow-up question, answer it with
further MCP queries (the same call budget applies to each reply). You are
strictly read-only: never call MCP tools that create, update, or delete
anything in PostHog — no dashboards, insights, feature flags, surveys, or
settings. Query and read tools only.
`,
  model: {
    provider_config: 'omnara-openrouter', // default model provider config in your org
    name: 'openai/gpt-5.6-sol', // configured model name on that provider config
  },
  mcp: {
    posthog: {
      // features=… narrows the server to the read-only query surface: the
      // query-* wrappers (insights), read-data-schema (data_schema), and
      // execute-sql (sql) — no create/update/delete tools are exposed at
      // all. mode=tools pins one MCP tool per PostHog tool (without it,
      // most clients get a single CLI-style tool). EU accounts: swap the
      // host for mcp-eu.posthog.com.
      url: 'https://mcp.posthog.com/mcp?mode=tools&features=insights,data_schema,sql',
      auth: { type: 'bearer', secret_id: secretId },
      permission: { mode: 'always_allow' }, // cron runs are headless; the exposed tool surface is read-only by construction
    },
  },
  tools: {
    send_integration_message: { permission: { mode: 'always_allow' } },
    set_integration_target: {},
  },
}

const { data: config } = await sdk.createAgentConfig({
  client,
  path,
  body: { source: JSON.stringify(agent), source_format: 'json' },
})
const { data: profiles } = await sdk.listAgentProfiles({
  client,
  path,
  query: { name: 'posthog-pulse-agent' },
})
const existingProfile = profiles.data.find((candidate) => candidate.name === 'posthog-pulse-agent')

const { data: profile } = existingProfile
  ? await sdk.updateAgentProfile({
      client,
      path: { ...path, agentProfileID: existingProfile.id },
      body: { config: config.id, expected_current_config_id: existingProfile.current_config_id },
    })
  : await sdk.createAgentProfile({
      client,
      path,
      body: { name: 'posthog-pulse-agent', config: config.id },
    })
console.log(existingProfile ? 'profile updated:' : 'profile created:', profile.id)

// %% [markdown]
// ## 4. Launch a pulse and watch it work
//
// Create an agent from the profile with a kickoff message, then follow its
// event stream and print each step: the PostHog MCP calls (they appear as
// `mcp__posthog__…` tools), the comparison against the trend, the pulse.
// `openAgentEventStream` is a real-time server-sent event stream — nothing
// to poll on our side.

// %%
const { data: launch } = await sdk.createAgent({
  client,
  path,
  body: {
    profile: profile.id,
    config: profile.current_config_id,
    message: 'Run the daily PostHog usage pulse now.',
  },
})
const agentPath = { ...path, agentID: launch.agent.id }
console.log('agent:  ', launch.agent.id)
console.log('console:', `https://app.omnara.com/projects/${project.id}/agents/${launch.agent.id}`)
console.log()

// Print events until the agent's turn ends — a model output whose stop
// reason is anything but a tool call. If the stream drops, reconnect from
// the last seen sequence.
let after = 0
for (let done = false; !done; ) {
  const { stream } = await openAgentEventStream({
    client,
    path: agentPath,
    query: { after_sequence: after },
  })
  try {
    for await (const frame of stream) {
      if (!('event_kind' in frame)) continue
      after = Math.max(after, frame.sequence)
      if (frame.event_kind === 'model_output') {
        for (const block of frame.content_blocks) {
          if (block.type === 'text' && block.text.trim()) console.log('\nagent:', block.text)
          else if (block.type === 'tool_call') console.log('\ntool:', block.name)
        }
        if (frame.stop_reason !== 'tool_use') {
          done = true // the turn ended: pulse delivered
          break
        }
      } else if (frame.event_kind === 'tool_result') {
        console.log('  ->', frame.outcome)
      }
    }
  } catch {
    await new Promise((resolve) => setTimeout(resolve, 1000))
  }
}

console.log('\nDone. The agent stays available — message it from the console or Slack anytime.')

// %% [markdown]
// ## 5. Connect Slack (one-time, optional)
//
// One-time setup:
//
// 1. Set `SLACK_APP_CONFIGURATION_TOKEN` in `.env` — create the token at
//    [api.slack.com/apps](https://api.slack.com/apps).
// 2. Rerun this file (or just this section) and open the printed OAuth URL
//    to install the Slack app.
// 3. Invite the bot to a channel and mention it.
//
// Daily pulses then land in Slack, and thread replies become instructions —
// "why did signups spike?" in the thread gets answered with fresh PostHog
// queries.

// %%
const slackAppConfigurationToken = env.SLACK_APP_CONFIGURATION_TOKEN ?? '' // xoxe.xoxp-... from https://api.slack.com/apps

if (slackAppConfigurationToken) {
  const { data: slack } = await sdk.createSlackSetup({
    client,
    path: { ...path, agentProfileID: profile.id },
    body: { app_name: 'PostHog Pulse', app_configuration_token: slackAppConfigurationToken },
  })
  console.log('open this URL to install the Slack app:')
  console.log(slack.oauth_url)
} else {
  console.log('skipped — set SLACK_APP_CONFIGURATION_TOKEN in .env to connect Slack')
}

// %% [markdown]
// ## 6. Make it daily
//
// Set `SCHEDULE_DAILY=1` in `.env` and rerun this section: it creates one
// cron trigger that launches a fresh pulse every morning at 9am. Each run
// reports on "yesterday" in your PostHog project's timezone and recomputes
// the 7-day baseline from scratch, so there is no state between runs.

// %%
// Opt-in: the cron trigger is only created when SCHEDULE_DAILY=1 is set.
if (env.SCHEDULE_DAILY === '1') {
  const { data: triggers } = await sdk.listCronTriggers({
    client,
    path,
    query: { name: 'posthog-pulse-agent-daily' },
  })
  const existingTrigger = triggers.data.find(
    (trigger) => trigger.name === 'posthog-pulse-agent-daily',
  )

  if (existingTrigger) {
    console.log(
      'cron trigger exists:',
      existingTrigger.id,
      '- next fire:',
      existingTrigger.next_fire_at,
    )
  } else {
    const { data: trigger } = await sdk.createCronTrigger({
      client,
      path,
      body: {
        name: 'posthog-pulse-agent-daily',
        target: { type: 'profile', agent_profile_id: profile.id },
        cron: '0 9 * * *',
        timezone: 'America/Los_Angeles',
        message_template: 'Run the daily PostHog usage pulse.',
      },
    })
    console.log('cron trigger created:', trigger.id, '- next fire:', trigger.next_fire_at)
  }
} else {
  console.log('skipped — set SCHEDULE_DAILY=1 in .env to schedule the daily pulse')
}

// %% [markdown]
// ---
//
// That's the whole system: one config object, a secret, and a cron trigger.
// The agent queries PostHog through PostHog's own MCP server — no machine,
// no curl, no API plumbing. The metrics and the report format are plain
// prompt text you can read and edit, and Slack is both the delivery surface
// and the steering wheel: reply in the thread to dig into any number.
//
// Just as important is what isn't here: no write access to PostHog (the
// tool surface is read-only by construction), no state between runs (each
// recomputes the 7-day average), and no unbounded report — two standing
// queries, a hard call budget, and "steady day" as a first-class outcome.
