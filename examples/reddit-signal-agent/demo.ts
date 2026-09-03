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
// A listening agent on Omnara: it searches Reddit for the last 24 hours of
// conversation about **whatever you care about** — a product category, a
// competitor, your own project — filters hard, and delivers a ≤5-item digest
// to Slack. The agent is one config object — no service to deploy. Run this
// file top to bottom and you have your own: pick the topic in one section,
// and the rest wires it up and launches a run using the Omnara TypeScript SDK
// (`@omnara/sdk`), on [Deno](https://docs.deno.com/runtime/).
//
// **No Reddit API key.** Reddit gates its Data API behind manual approval, so
// this agent fetches through the [Apify MCP server](https://mcp.apify.com)
// backed by a maintained Reddit scraper
// ([trudax/reddit-scraper-lite](https://apify.com/trudax/reddit-scraper-lite),
// pay-per-result). It also needs **no machine pool** — where the X signal
// agent runs `curl` on a machine, this agent's entire fetch layer is the MCP
// server. An Apify account token is the only credential.
//
// Prereqs:
//
// - An Omnara account ([app.omnara.com](https://app.omnara.com)) with a personal
//   access token, and a free Apify account
//   ([console.apify.com](https://console.apify.com/sign-up), no credit card) —
//   the API token is under **Settings → Integrations**.
// - `cp .env.example .env` in this folder, with `OMNARA_API_KEY` and
//   `APIFY_TOKEN` set.
// - Deno: `brew install deno`, then `deno install` once in this folder to
//   fetch [`@omnara/sdk`](https://www.npmjs.com/package/@omnara/sdk) (see
//   `package.json`). Run the demo with `deno run --allow-all demo.ts` — or
//   open it as a notebook: this file is jupytext percent format, and
//   `deno jupyter --install` registers the Deno kernel.

// %%
import { load } from 'jsr:@std/dotenv'
import { bearerToken, createOmnaraClient, openAgentEventStream, sdk } from '@omnara/sdk'

const env = await load()

const client = createOmnaraClient({
  baseUrl: 'https://app.omnara.com',
  auth: bearerToken(env.OMNARA_API_KEY),
})

// %% [markdown]
// ## 1. Where it lives
//
// New accounts come with a default org and a default project — take the first
// of each. That's all this agent needs: unlike the X signal agent there is no
// machine pool here, because the agent never runs a shell command. Its only
// tools are the Apify MCP server and Omnara's built-in web tools.

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
// The Apify API token is stored as a project secret. The agent config will
// reference only the `sec_…` ID — Omnara sends the value as the bearer token
// on every request to the MCP server, and it never appears in the config or
// the event log.

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
// This is the only section to edit to make the agent yours. `topic` steers the
// filtering rules; `searches` and `subreddits` control what the scrape
// returns — keyword phrases swept across all of Reddit (no boolean syntax;
// one phrase per entry), plus the new-post feeds of the communities where
// the conversation lives. That last part is Reddit's edge over X: your
// audience clusters in a few known places — use it. The default listens for
// managed-agents conversation — swap in whatever you want to track, then run
// the rest of the file as usual.

// %%
// What the agent listens for. topic steers the filtering; searches are
// swept across all of Reddit (no OR syntax — one phrase per entry); each
// subreddit's new-post feed is scanned even when search would miss it.
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
// An instruction (templated with your topic and searches from above), a model,
// the tools, and the MCP server it fetches through. The `mcp.reddit` block
// points at Apify's hosted MCP server with the URL pinned to exactly one
// scraper — the server then also exposes the run helpers (`get-actor-run`,
// `get-dataset-items`) the flow needs, and nothing else. Omnara authenticates
// every MCP request with the Apify token from the secret above.
//
// A scan is a three-step tool flow (start the scraper run → poll until it
// finishes → fetch the dataset), and the instruction walks the agent through
// it. One run covers both the keyword searches and the subreddit feeds, with
// hard caps (`maxItems`, one run per scan) so it can never overshoot the
// budget: 120 results ≈ $0.40 against Apify's free $5/month. Set the `model`
// names from your console's **Models** page.

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
id. Each item's url field is the reddit.com thread permalink; its link
field may be a media file — always cite url. Subreddit-feed items are not
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
// event stream and print what it does: the scraper run, the polling, the
// filtering, the digest. The SDK's `openAgentEventStream` is a real-time
// server-sent event stream — no polling on our side.
//
// Reddit scraper runs take a few minutes (it's a real browser behind the MCP
// server), so expect a quiet stretch of `get-actor-run` calls in the middle —
// that's the agent waiting on the scrape, not a hang.

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

// Stream events until the agent's turn ends (a model output that stops for
// anything other than a tool call). Reconnects from the last seen sequence
// if the stream drops.
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
// Set `SLACK_APP_CONFIGURATION_TOKEN` in `.env` to a Slack **app configuration
// token** from [api.slack.com/apps](https://api.slack.com/apps) and rerun this
// file — it creates the Slack app and prints an OAuth URL to approve. Then
// **invite the bot to a channel** (`/invite @your-bot`) and mention it — that
// launches an agent, and its digest lands in that thread. Messages route to
// wherever the agent was mentioned or DM'd; there is no default channel.
// Thread replies become agent inputs, so the team can ask for reply drafts
// right in the thread.

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
// Opt-in for script runs: set `SCHEDULE_DAILY=1` in `.env`. One cron
// trigger and this runs every weekday morning without any of the code
// above — each firing launches a fresh agent from the profile, and runs scan
// non-overlapping 24-hour windows (`"time": "day"` in the scraper input) so
// there is no dedupe state to keep.
//
// Note: agents launched from the profile have no Slack thread, so their
// digests appear in the Omnara console. For daily digests in a Slack channel,
// mention the bot there once — each firing then delivers to that thread.

// %%
// Scheduling is opt-in for a straight top-to-bottom run: set
// SCHEDULE_DAILY=1 in .env to create the weekday cron trigger.
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
// That's the whole system: one config object, one secret, and a cron trigger.
// The agent fetches Reddit through a hosted MCP server — no Reddit API key, no
// machine, no scraper to maintain — the filter rules are prompt engineering
// you can read, and Slack is the delivery surface and steering wheel.
//
// If you outgrow the scraper (higher volume, stricter compliance needs), the
// fetch layer is one `mcp` block: swap in a different provider — e.g. Bright
// Data's Reddit Scraper API via a machine + `curl`, like the
// [X signal agent](../x-signal-agent) does — without touching the profile,
// Slack, or cron sections.
