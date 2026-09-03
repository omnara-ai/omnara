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
// # Reddit Signal Agent — build your own
//
// An agent that watches Reddit for you. Each run it gathers the last 24
// hours of posts about a topic you pick, filters out the noise, and delivers
// a digest of at most five threads — each with why it matters and a
// suggested action. The whole agent is one config object; there is no
// service to deploy. Run this file top to bottom and you have your own.
//
// **No Reddit API key.** Reddit's Data API requires manual approval, so this
// agent fetches through [Apify's hosted MCP server](https://mcp.apify.com)
// running a maintained scraper,
// [trudax/reddit-scraper-lite](https://apify.com/trudax/reddit-scraper-lite).
// It also needs **no machine pool**: the MCP server is the entire fetch
// layer. An Apify account token is the only credential.
//
// Before you run it:
//
// 1. Get an Omnara personal access token ([app.omnara.com](https://app.omnara.com)).
// 2. Get a free Apify token ([console.apify.com](https://console.apify.com/sign-up),
//    no credit card) — it's under **Settings → Integrations**.
// 3. `cp .env.example .env` and fill in `OMNARA_API_KEY` and `APIFY_TOKEN`.
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
// ## 1. Where it lives
//
// Every account has a default org and a default project; the agent lives in
// the first of each. Nothing else to provision — this agent never runs shell
// commands, so it needs no machine. Its only tools are the Apify MCP server
// and Omnara's built-in web tools.

// %%
const { data: me } = await sdk.getCurrentUser({ client })
const org = me.orgs[0]
const { data: projects } = await sdk.listVisibleProjects({ client, path: { orgID: org.id } })
const project = projects.data[0]
const path = { orgID: org.id, projectID: project.id }

console.log('org:    ', org.name, org.id)
console.log('project:', project.name, project.id)

// %% [markdown]
// ## 2. The Apify token becomes a secret
//
// The token becomes a project secret, and the agent config references only
// its `sec_…` ID. Omnara sends the value as the bearer token on each MCP
// request; it never appears in the config or the event log. Rerunning this
// section rotates the secret to the current `.env` value.

// %%
if (!env.APIFY_TOKEN) throw new Error('set APIFY_TOKEN in .env')
const secretName = 'reddit-signal-agent-apify-token'
const material = { kind: 'generic' as const, value: env.APIFY_TOKEN }

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
// This is the only section you need to edit. Three knobs:
//
// - `topic` — what the agent judges relevance against.
// - `searches` — keyword phrases searched across all of Reddit. Reddit
//   search has no boolean syntax, so keep one plain phrase per entry.
// - `subreddits` — communities whose newest posts are scanned even when
//   keyword search would miss them. This is Reddit's edge over X: your
//   audience gathers in a few known places.
//
// The default listens for managed-agents conversation. Swap in your own,
// then run the rest of the file as usual.

// %%
// Edit these three to make the agent yours.
const topic = 'managed-agent infrastructure'
const searches = [
  'managed agents',
  'agent infrastructure',
  'durable agents',
  'agent control plane',
  'Vercel Eve',
]
const subreddits = [
  'AI_Agents', 'LLMDevs', 'LangChain', 'AgentsOfAI', 'ClaudeAI',
  'mcp', 'crewai', 'LocalLLaMA',
]
console.log('listening for:', topic)

// %% [markdown]
// ## 4. The agent — this object is the whole thing
//
// One object holds everything: the instruction (templated with your topic
// and searches), the model, the tools, and the MCP server it fetches
// through.
//
// The `mcp.reddit` URL pins Apify's MCP server to exactly one scraper. The
// server also exposes the two run helpers the flow needs (`get-actor-run`
// and `get-dataset-items`) and nothing else, and Omnara authenticates every
// request with the secret from step 2.
//
// A scan is three tool calls: start the scraper run, poll until it
// finishes, fetch the results. One run covers both the keyword searches and
// the subreddit feeds, and `maxItems` caps the spend: 120 results ≈ $0.40
// against Apify's free $5/month. Set `model` to names from your console's
// **Models** page.

// %%
const agent = {
  instruction: `
You are a category-listening agent for a team interested in: ${topic}.
Each run: search Reddit for the last 24 hours of conversation, filter hard,
and deliver a short digest.

Fetch. Your Reddit access is the "reddit" MCP server. Start the scrape by
calling trudax--reddit-scraper-lite with exactly:

  {
    "searches": ${JSON.stringify(searches)},
    "startUrls": ${JSON.stringify(subreddits.map((name) => ({ url: `https://www.reddit.com/r/${name}/new/` })))},
    "searchPosts": true, "searchComments": false,
    "searchCommunities": false, "searchUsers": false,
    "skipComments": true, "skipUserPosts": true, "skipCommunity": true,
    "sort": "new", "time": "day",
    "maxItems": 120, "maxPostCount": 10,
    "proxy": { "useApifyProxy": true }
  }

The call returns run metadata, not posts. Runs take several minutes: poll
get-actor-run with the returned runId and waitSecs 45 until status is
SUCCEEDED, then fetch the posts with get-dataset-items using the dataset
id and limit 200. Always pass that limit: the tool returns only a small
first page by default, silently dropping the rest — compare the returned
total against the number of items you received and fetch the remainder
with offset if any are missing. Each item's url field is the reddit.com
thread permalink; its link field may be a media file — always cite url. Subreddit-feed items are not
limited to the past day: discard anything whose createdAt is more than 24
hours older than the newest item in the dataset.

Start exactly one scrape per run — results are billed. If the run ends
FAILED or ABORTED, report the error and stop; do not retry in a loop.

Filter. Classify every post as builder-signal (someone building, comparing,
or asking about ${topic}), commentary, engagement-bait, or
irrelevant. Reddit keyword search matches loosely — expect posts from
entirely unrelated subreddits; discard them silently. Only builder-signal
and unusually good commentary survive. High upvote counts are not signal
on their own. When knowing who an author is would change the verdict, you
may use web_search or web_fetch to check — a few lookups per run at most,
only for posts that made the cut.

Digest. At most 5 items; fewer is better. An empty digest ("nothing worth
your time today") is a good outcome — never pad it. For each item:
- one-line summary
- why it matters to this team
- author: u/username in r/subreddit
- link: the post's url field (the reddit.com thread)
- suggested action: reply, track the author, or ignore

Deliver. When this conversation is driven through an integration such as
Slack, send the digest with send_integration_message — the external user
only sees messages sent that way. Otherwise present the digest directly in
the conversation. If someone replies asking for a draft, write the reply
text for a human to post. Never post to Reddit yourself.
`,
  model: {
    provider_config: 'omnara-openrouter', // default model provider config in your org
    name: 'openai/gpt-5.6-sol', // configured model name on that provider config
  },
  mcp: {
    reddit: {
      url: 'https://mcp.apify.com/?tools=trudax/reddit-scraper-lite',
      auth: { type: 'bearer' as const, secret_id: secretId },
      permission: { mode: 'always_allow' },
    },
  },
  tools: {
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
  query: { name: 'reddit-signal-agent' },
})
const existingProfile = profiles.data.find((candidate) => candidate.name === 'reddit-signal-agent')

const { data: profile } = existingProfile
  ? await sdk.updateAgentProfile({
      client,
      path: { ...path, agentProfileID: existingProfile.id },
      body: { config: config.id, expected_current_config_id: existingProfile.current_config_id },
    })
  : await sdk.createAgentProfile({
      client,
      path,
      body: { name: 'reddit-signal-agent', config: config.id },
    })
console.log(existingProfile ? 'profile updated:' : 'profile created:', profile.id)

// %% [markdown]
// ## 5. Launch a scan and watch it work
//
// Create an agent from the profile with a kickoff message, then follow its
// event stream and print each step: the scraper run, the polling, the
// filtering, the digest. `openAgentEventStream` is a real-time server-sent
// event stream — nothing to poll on our side.
//
// The scrape itself takes a few minutes (a real browser runs behind the MCP
// server), so expect a quiet stretch of `get-actor-run` calls in the middle.
// That's the agent waiting on the scrape, not a hang.

// %%
const { data: launch } = await sdk.createAgent({
  client,
  path,
  body: {
    profile: profile.id,
    config: profile.current_config_id,
    message: `Run the ${topic} Reddit scan now.`,
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
    body: { app_name: 'Reddit Signal Agent', app_configuration_token: slackAppConfigurationToken },
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
// scans its own 24-hour window (`"time": "day"` in the scraper input), so
// there is no dedupe state to keep.
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
    query: { name: 'reddit-signal-agent-daily' },
  })
  const existingTrigger = triggers.data.find((trigger) => trigger.name === 'reddit-signal-agent-daily')

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
        name: 'reddit-signal-agent-daily',
        target: { type: 'profile', agent_profile_id: profile.id },
        cron: '0 9 * * 1-5',
        timezone: 'America/Los_Angeles',
        message_template: `Run the daily ${topic} Reddit scan.`,
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
// That's the whole system: one config object, one secret, and a cron
// trigger. No Reddit API key, no machine, no scraper code to maintain. The
// filter rules are plain prompt text you can read and edit, and Slack is
// both the delivery surface and the steering wheel.
//
// If you outgrow the scraper (more volume, stricter compliance), the fetch
// layer is just the `mcp` block: swap in another provider — for example
// Bright Data via a machine and `curl`, the way the
// [X signal agent](../x-signal-agent) fetches — without touching the
// profile, Slack, or cron sections.
