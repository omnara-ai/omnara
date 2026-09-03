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
// # X Signal Agent — build your own
//
// An agent that watches X (Twitter) for you. Each run it searches the last
// 24 hours of posts about a topic you pick, filters out the noise, and
// delivers a digest of at most five posts — each with why it matters and a
// suggested action. The whole agent is one config object; there is no
// service to deploy. Run this file top to bottom and you have your own.
//
// Before you run it:
//
// 1. Get an Omnara personal access token ([app.omnara.com](https://app.omnara.com)).
// 2. Get an X API bearer token with pay-per-use billing
//    ([console.x.com](https://console.x.com)) — a daily scan costs on the
//    order of cents.
// 3. `cp .env.example .env` and fill in `OMNARA_API_KEY` and `X_BEARER_TOKEN`.
// 4. Install [Deno](https://docs.deno.com/runtime/) (`brew install deno`),
//    then run `deno install` once in this folder to fetch
//    [`@omnara/sdk`](https://www.npmjs.com/package/@omnara/sdk).
//
// Then: `deno run --allow-all demo.ts`. Prefer notebook cells? This file is
// jupytext percent format — open it in Jupyter with the Deno kernel
// (`deno jupyter --install`).

// %%
import { load } from 'jsr:@std/dotenv'
import { bearerToken, createOmnaraClient, openAgentEventStream, sdk } from '@omnara/sdk'

const env = await load()

const client = createOmnaraClient({
  baseUrl: 'https://api.omnara.com/v1',
  auth: bearerToken(env.OMNARA_API_KEY),
})

// %% [markdown]
// ## 1. Where it deploys
//
// Every account has a default org, a default project, and a managed machine
// pool already granted. The agent uses the first of each — the machine is
// where it runs `curl` against the X API.

// %%
const { data: me } = await sdk.getCurrentUser({ client })
const org = me.orgs[0]
const { data: projects } = await sdk.listVisibleProjects({ client, path: { orgID: org.id } })
const project = projects.data[0]
const path = { orgID: org.id, projectID: project.id }
const { data: grants } = await sdk.listProjectMachinePoolGrants({ client, path })
const pool = grants.data[0].machine_pool

console.log('org:    ', org.name, org.id)
console.log('project:', project.name, project.id)
console.log('pool:   ', pool.name)

// %% [markdown]
// ## 2. The X token becomes a secret
//
// The bearer token becomes a project secret, and the agent config references
// only its `sec_…` ID. Omnara injects the value on the machine at runtime as
// the `X_BEARER_TOKEN` environment variable; it never appears in the config
// or the event log. Rerunning this section rotates the secret to the current
// `.env` value.

// %%
if (!env.X_BEARER_TOKEN) throw new Error('set X_BEARER_TOKEN in .env')
const secretName = 'x-signal-agent-bearer-token'
const material = { kind: 'generic' as const, value: env.X_BEARER_TOKEN }

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
// ## 3. Pick what to listen for
//
// This is the only section you need to edit. Two knobs:
//
// - `topic` — what the agent judges relevance against.
// - `xQuery` — an [X search query](https://docs.x.com/x-api/posts/search/integrate/build-a-query)
//   that controls what the search returns.
//
// The default listens for managed-agents conversation. Swap in your own,
// then run the rest of the file as usual.

// %%
// Keep single quotes out of xQuery: the agent passes it inside a
// single-quoted shell argument.
const topic = 'managed-agent infrastructure'
const xQuery =
  '("managed agents" OR "agent infrastructure" OR "durable agents") -is:retweet lang:en'
console.log('listening for:', topic)

// %% [markdown]
// ## 4. The agent — this object is the whole thing
//
// One object holds everything: the instruction (templated with your topic
// and query), the model, the tools, and where it runs (the machine pool and
// secret from the sections above). The agent fetches X by running `curl`
// itself, and the filter rules and the five-item cap are plain prompt text
// in the instruction. Set `model` to names from your console's **Models**
// page.

// %%
const agent = {
  instruction: `
You are a category-listening agent for a team interested in: ${topic}.
Each run: search X for the last 24 hours of conversation, filter hard, and
deliver a short digest.

Fetch. Use curl with the X_BEARER_TOKEN environment variable (never print
it) against the X API v2 recent search endpoint:

  curl -sS --get 'https://api.x.com/2/tweets/search/recent' \\
    --data-urlencode 'query=${xQuery}' \\
    --data-urlencode "start_time=$(date -u -d '24 hours ago' +%Y-%m-%dT%H:%M:%SZ)" \\
    --data-urlencode 'max_results=50' \\
    --data-urlencode 'tweet.fields=public_metrics,created_at,author_id' \\
    --data-urlencode 'expansions=author_id' \\
    --data-urlencode 'user.fields=username,name,description' \\
    -H "Authorization: Bearer $X_BEARER_TOKEN"

Make exactly one search request per run — reads are billed. If the request
fails, report the response body and stop; do not retry in a loop.

Filter. Classify every post as builder-signal (someone building, comparing,
or asking about ${topic}), commentary, engagement-bait, or
irrelevant. Only builder-signal and unusually good commentary survive.
High engagement counts are not signal on their own. When knowing who an
author is would change the verdict, you may use web_search or web_fetch to
check — a few lookups per run at most, only for posts that made the cut.

Digest. At most 5 items; fewer is better. An empty digest ("nothing worth
your time today") is a good outcome — never pad it. For each item:
- one-line summary
- why it matters to this team
- author: @username and who they appear to be
- link: https://x.com/i/status/<tweet id>
- suggested action: reply, track the author, or ignore

Deliver. When this conversation is driven through an integration such as
Slack, send the digest with send_integration_message — the external user
only sees messages sent that way. Otherwise present the digest directly in
the conversation. If someone replies asking for a draft, write the reply
text for a human to post. Never post to X yourself.
`,
  model: {
    provider_config: 'omnara-openrouter', // default model provider config in your org
    name: 'openai/gpt-5.6-sol', // configured model name on that provider config
  },
  machine_sources: [
    { machine_pool_name: pool.name, secret_env_overlay: { X_BEARER_TOKEN: secretId } },
  ],
  tools: {
    // run_command plus the process/machine tools Omnara recommends whenever
    // machine_sources is set (the API warns if they're missing)
    run_command: { permission: { mode: 'always_allow' } },
    read_process: {},
    write_process: {},
    stop_process: {},
    list_processes: {},
    list_machines: {},
    inspect_machine: {},
    web_search: {},
    web_fetch: {},
    send_integration_message: { permission: { mode: 'always_allow' } },
    set_integration_target: {},
  },
}

const { data: config } = await sdk.createAgentConfig({
  client,
  path,
  body: { source: JSON.stringify(agent), source_format: 'json' },
})
for (const warning of config.warnings ?? []) console.warn('config warning:', warning.message)
const { data: profiles } = await sdk.listAgentProfiles({
  client,
  path,
  query: { name: 'x-signal-agent' },
})
const existingProfile = profiles.data.find((candidate) => candidate.name === 'x-signal-agent')

const { data: profile } = existingProfile
  ? await sdk.updateAgentProfile({
      client,
      path: { ...path, agentProfileID: existingProfile.id },
      body: { config: config.id, expected_current_config_id: existingProfile.current_config_id },
    })
  : await sdk.createAgentProfile({
      client,
      path,
      body: { name: 'x-signal-agent', config: config.id },
    })
console.log(existingProfile ? 'profile updated:' : 'profile created:', profile.id)

// %% [markdown]
// ## 5. Launch a scan and watch it work
//
// Create an agent from the profile with a kickoff message, then follow its
// event stream and print each step: the `curl` against X, the filtering,
// the digest. `openAgentEventStream` is a real-time server-sent event
// stream — nothing to poll on our side.

// %%
const { data: launch } = await sdk.createAgent({
  client,
  path,
  body: {
    profile: profile.id,
    config: profile.current_config_id,
    message: `Run the ${topic} X scan now.`,
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
          done = true // the turn ended: digest delivered
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
// ## 6. Connect Slack (one-time, optional)
//
// One-time setup:
//
// 1. Set `SLACK_APP_CONFIGURATION_TOKEN` in `.env` — create the token at
//    [api.slack.com/apps](https://api.slack.com/apps).
// 2. Rerun this file (or just this section) and open the printed OAuth URL
//    to install the Slack app.
// 3. Invite the bot to a channel (`/invite @your-bot`) and mention it.
//
// Mentioning the bot launches an agent, and its digest lands in that
// thread. There is no default channel — the agent answers wherever it is
// mentioned or DM'd, and thread replies become instructions to it (ask for
// a reply draft right in the thread).

// %%
const slackAppConfigurationToken = env.SLACK_APP_CONFIGURATION_TOKEN ?? '' // xoxe.xoxp-... from https://api.slack.com/apps

if (slackAppConfigurationToken) {
  const { data: slack } = await sdk.createSlackSetup({
    client,
    path: { ...path, agentProfileID: profile.id },
    body: { app_name: 'X Signal Agent', app_configuration_token: slackAppConfigurationToken },
  })
  console.log('open this URL to install the Slack app:')
  console.log(slack.oauth_url)
} else {
  console.log('skipped — set SLACK_APP_CONFIGURATION_TOKEN in .env to connect Slack')
}

// %% [markdown]
// ## 7. Make it daily (optional)
//
// Set `SCHEDULE_DAILY=1` in `.env` and rerun this section: it creates one
// cron trigger that launches a fresh scan every weekday at 9am. Each run
// searches its own 24-hour window, so there is no dedupe state to keep.
//
// Scheduled runs have no Slack thread, so their digests land in the Omnara
// console. For daily digests in a Slack channel, mention the bot there once
// — each firing then delivers to that thread.

// %%
// Opt-in: the cron trigger is only created when SCHEDULE_DAILY=1 is set.
if (env.SCHEDULE_DAILY === '1') {
  const { data: triggers } = await sdk.listCronTriggers({
    client,
    path,
    query: { name: 'x-signal-agent-daily' },
  })
  const existingTrigger = triggers.data.find((trigger) => trigger.name === 'x-signal-agent-daily')

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
        name: 'x-signal-agent-daily',
        target: { type: 'profile', agent_profile_id: profile.id },
        cron: '0 9 * * 1-5',
        timezone: 'America/Los_Angeles',
        message_template: `Run the daily ${topic} X scan.`,
      },
    })
    console.log('cron trigger created:', trigger.id, '- next fire:', trigger.next_fire_at)
  }
} else {
  console.log('skipped — set SCHEDULE_DAILY=1 in .env to schedule the weekday scan')
}

// %% [markdown]
// ---
//
// That's the whole system: one config object, a secret, and a cron trigger.
// The agent fetches X itself with `curl`, the filter rules are plain prompt
// text you can read and edit, and Slack is both the delivery surface and
// the steering wheel.
