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
// # LinkedIn Signal Agent — build your own
//
// A listening agent on Omnara: it searches LinkedIn for the last 24 hours of
// posts about **whatever you care about** — a product category, a competitor,
// your own project — filters hard, and delivers a ≤5-item digest to Slack. The
// agent is one config object — no service to deploy. Run this file top to
// bottom and you have your own: pick the topic in one section, and the rest wires
// it up and launches a run using the Omnara TypeScript SDK (`@omnara/sdk`), on
// [Deno](https://docs.deno.com/runtime/).
//
// **No LinkedIn API key, no LinkedIn account.** LinkedIn's official API has no
// public post search, and cookie-based tools risk the account behind the
// cookie. This agent fetches through the [Apify MCP server](https://mcp.apify.com)
// backed by two no-cookie scrapers —
// [linkedin-post-search](https://apify.com/harvestapi/linkedin-post-search)
// for keywords,
// [linkedin-profile-posts](https://apify.com/harvestapi/linkedin-profile-posts)
// for tracked feeds — and, like the
// [Reddit signal agent](../reddit-signal-agent), needs **no machine pool**:
// the entire fetch layer is the MCP server. An Apify account token is the
// only credential.
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
// of each. That's all this agent needs: like the Reddit signal agent there is
// no machine pool here, because the agent never runs a shell command. Its only
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
// the event log. (If you already ran the Reddit example, this is a second,
// independently rotatable secret.)

// %%
if (!env.APIFY_TOKEN) throw new Error('set APIFY_TOKEN in .env')
const secretName = 'linkedin-signal-agent-apify-token'
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
// This is the only section to edit to make the agent yours. Three knobs:
//
// - `context` — who you are and who your audience is. The agent judges
//   relevance against this, and it matters more than the queries: LinkedIn
//   keyword search returns plenty of adjacent hype, so precision comes from
//   the filter.
// - `searchQueries` — LinkedIn post searches. Boolean syntax works (up to 5
//   operators and 500 characters per query), and **cost scales per query**,
//   so prefer one boolean query over many single ones. Empty array skips
//   keyword search.
// - `targets` — profile and company-page URLs whose last-day posts get
//   scanned even when keyword search would miss them — the analogue of the
//   Reddit agent's subreddits. Empty skips.
//
// The default listens for agent-infrastructure conversation on behalf of
// Omnara — swap in your own, then run the rest of the file as usual.

// %%
// What the agent listens for. context drives the filtering; searchQueries
// is the keyword sweep (billed per query — keep it to one or two); targets
// are company/profile feeds scanned alongside the search.
const topic = 'managed-agent infrastructure'
const context = `
Omnara (app.omnara.com, github.com/omnara-ai/omnara) is an open-source
platform for creating and interacting with production-ready agents via an
API — the agent control plane teams otherwise build themselves. It is an
open-source alternative to Claude Managed Agents, LangChain Managed Agents,
and Vercel Eve. Users bring their own machines (an EC2 box, a laptop, or
sandbox providers like Blaxel/Daytona/Unikraft) and their own LLM endpoints,
and Omnara handles the control-plane work: agent APIs, surviving machine
failures, resumption and durable execution, sandbox provisioning, skills
and MCP wiring, message tracking, and audit logs. The audience is teams
building agent products — using Claude Agent SDK, Mastra, LangChain/
LangGraph, or their own loop — who are otherwise stuck building and
maintaining this infrastructure themselves. The sharpest demand signal is
a team describing building or maintaining exactly that in-house: hosting
agents on VMs or Kubernetes, handling machine failures and resumption
themselves, provisioning sandboxes (mentions of Blaxel, Daytona, or
Unikraft are a strong tell), or wiring their own agent API, skills, and
MCP layer.
`
// The category phrases plus the most-compared competitor, in one boolean
// query (LinkedIn allows 5 operators per query — this uses 4).
const searchQueries = [
  '"managed agents" OR "agent infrastructure" OR "durable agents" OR "agent control plane" OR "Vercel Eve"',
]
// Watched regardless of keywords — competitors and category voices.
const targets = [
  'https://www.linkedin.com/company/langchain/',
  'https://www.linkedin.com/company/vercel/',
  'https://www.linkedin.com/company/anthropic/',
]
console.log('listening for:', topic)

// %% [markdown]
// ## 4. The agent — this object is the whole thing
//
// An instruction (templated with your context, queries, and targets from
// above), a model, the tools, and the MCP server it fetches through. The
// `mcp.linkedin` block pins Apify's hosted MCP server to exactly the two
// scrapers plus their run helpers (`get-actor-run`, `get-dataset-items`),
// and nothing else. Omnara authenticates every MCP request with the Apify
// token from the secret above.
//
// A scan starts each scraper once, polls both runs, then fetches and pools
// the two datasets. `postedLimit: "24h"` scopes both server-side, and
// `maxPosts` hard-caps the spend: 200 search posts + 10 per tracked feed
// ≈ 260 posts ≈ $0.52 per scan (~$11/month on a weekday cron — past Apify's
// free $5 credit). The generous sweep cap matters: broad phrases can match
// 200+ posts a day, and with `sortBy: "date"` a tight cap silently drops the
// day's oldest matches — for a morning scan, that's yesterday's US business
// hours. Set the `model` names from your console's **Models** page.

// %%
const agent = {
  instruction: `
You are a category-listening agent for the team building the following
product:
${context}
Each run: gather the last 24 hours of LinkedIn posts in this space, filter
hard, and deliver a short digest.

Fetch. Your LinkedIn access is the "linkedin" MCP server — two Apify
scrapers plus their run helpers. A scan starts each configured scraper
once, then polls and fetches each:

1. Keyword sweep. ${searchQueries.length ? `Call harvestapi--linkedin-post-search with exactly:
   {
     "searchQueries": ${JSON.stringify(searchQueries)},
     "postedLimit": "24h",
     "sortBy": "date",
     "maxPosts": 200,
     "profileScraperMode": "short",
     "scrapeReactions": false,
     "scrapeComments": false
   }` : 'No search queries are configured — never call harvestapi--linkedin-post-search.'}
2. Tracked feeds. ${targets.length ? `Call harvestapi--linkedin-profile-posts with exactly:
   {
     "targetUrls": ${JSON.stringify(targets)},
     "postedLimit": "24h",
     "maxPosts": 10,
     "scrapeReactions": false,
     "scrapeComments": false
   }
   These are the posts of watched companies and voices, pulled even when
   they share no keywords with the search.` : 'No tracked feeds are configured — never call harvestapi--linkedin-profile-posts.'}
3. Each call returns run metadata (runId, dataset id), not the posts.
   Scraper runs can take a few minutes: keep polling get-actor-run with
   each runId and waitSecs 45 until its status is SUCCEEDED. Never start
   a second copy of a scrape because the first seems slow.
4. Fetch each run's posts with get-dataset-items using its dataset id,
   then pool the two sets and drop duplicates by linkedinUrl (a post can
   match both). Each item has content (the post text), linkedinUrl (the
   post permalink), postedAt.date, engagement (likes, comments, shares),
   and author with name, publicIdentifier, info (their headline), and
   linkedinUrl.

Start each scrape at most once per scan — results are billed. If a run
ends FAILED or ABORTED, report the error, continue with the other
dataset if you have one, and never retry the failed run.

Recency: postedLimit already scopes the scrape to the past day, but treat
postedAt.date as the source of truth and discard anything older than 24
hours if it slips through.

Filter. LinkedIn keyword search surfaces a lot of content marketing —
expect it and discard it silently. Judge every post strictly against the
team context above, and classify it:
- demand-signal: someone with the problem this team solves, asking for
  help or describing the pain firsthand — a potential user or a
  conversation worth joining.
- practitioner-signal: firsthand experience in this space — real setups,
  tool and platform comparisons, approaches and trade-offs, war stories
  with substance.
- industry-signal: competitor launches, funding, benchmarks, or notable
  research in the category.
- Everything else is noise, even when it shares keywords with the topic.
  LinkedIn-specific noise to drop on sight: engagement-bait ("Agree?",
  carousel listicles, reposted hot takes), agency and course lead-gen,
  hiring posts, event promos, motivational AI hype, and vendor content
  marketing dressed up as insight — including posts that read as ads for
  a competitor unless they carry real industry-signal.
Posts from the tracked feeds are held to the same bar: a watched
company's routine marketing is still noise — keep only genuine launches,
benchmarks, research, or substantive commentary.
Only the first three categories survive, and only when specific and
recent. Like counts on day-old posts are weak signal — judge the content.
The author's headline (author.info) is included with every post — use it;
when knowing more about an author would change the verdict, you may use
web_search or web_fetch to check — a few lookups per run at most, only for
posts that made the cut.

Digest. At most 5 items; fewer is better. Work in two passes: first
shortlist every post that plausibly survives the filter (typically 10–20),
then pick the final 5 from the shortlist, ranked demand-signal first, then
practitioner-signal, then industry-signal — a firsthand account of
building, running, or migrating this kind of infrastructure outranks any
vendor launch or funding news, and at most 2 industry-signal items may
appear per digest. An empty digest ("nothing worth your time today") is a
good outcome — never pad it. For each item:
- one-line summary
- why it matters to this team
- author: name and headline (from author.info)
- link: the item's linkedinUrl field
- suggested action: reply, track the author, or ignore

Deliver. When this conversation is driven through an integration such as
Slack, send the digest with send_integration_message — the external user
only sees messages sent that way. Otherwise present the digest directly in
the conversation. If someone replies asking for a draft, write the reply
text for a human to post — helpful, never salesy, always disclosing the
affiliation. Never post to LinkedIn yourself.
`,
  model: {
    provider_config: 'omnara-openrouter', // default model provider config in your org
    name: 'openai/gpt-5.6-sol', // configured model name on that provider config
  },
  mcp: {
    linkedin: {
      url: 'https://mcp.apify.com/?tools=harvestapi/linkedin-post-search,harvestapi/linkedin-profile-posts',
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
  query: { name: 'linkedin-signal-agent' },
})
const existingProfile = profiles.data.find(
  (candidate) => candidate.name === 'linkedin-signal-agent',
)

const { data: profile } = existingProfile
  ? await sdk.updateAgentProfile({
      client,
      path: { ...path, agentProfileID: existingProfile.id },
      body: { config: config.id, expected_current_config_id: existingProfile.current_config_id },
    })
  : await sdk.createAgentProfile({
      client,
      path,
      body: { name: 'linkedin-signal-agent', config: config.id },
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
// LinkedIn scraper runs take a few minutes, so expect a quiet stretch of
// `get-actor-run` calls in the middle — that's the agent waiting on the
// scrape, not a hang.

// %%
const { data: launch } = await sdk.createAgent({
  client,
  path,
  body: {
    profile: profile.id,
    config: profile.current_config_id,
    message: `Run the ${topic} LinkedIn scan now.`,
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
    body: {
      app_name: 'LinkedIn Signal Agent',
      app_configuration_token: slackAppConfigurationToken,
    },
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
// non-overlapping 24-hour windows (`postedLimit: "24h"` in the scraper input)
// so there is no dedupe state to keep.
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
    query: { name: 'linkedin-signal-agent-daily' },
  })
  const existingTrigger = triggers.data.find(
    (trigger) => trigger.name === 'linkedin-signal-agent-daily',
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
        name: 'linkedin-signal-agent-daily',
        target: { type: 'profile', agent_profile_id: profile.id },
        cron: '0 9 * * 1-5',
        timezone: 'America/Los_Angeles',
        message_template: `Run the daily ${topic} LinkedIn scan.`,
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
// The agent fetches LinkedIn through a hosted MCP server — no LinkedIn API
// key, no LinkedIn account or cookie, no machine, no scraper to maintain —
// the filter rules are prompt engineering you can read, and Slack is the
// delivery surface and steering wheel.
//
// Together with the [X](../x-signal-agent) and
// [Reddit](../reddit-signal-agent) signal agents this makes a family: same
// profile/Slack/cron skeleton, different fetch layer per platform — one
// `mcp` block to swap if this scraper breaks or you outgrow it. One standing
// caveat, sharper on LinkedIn than anywhere: scraping avoids the API-key
// blocker, not the platform's terms — keep this internal-facing.
