# In-app guide

An agent that lives inside your web app and shows your users how to do things on the
page they are on. They ask a question; a cursor moves to the right control, highlights
it, and tells them what to do, one step at a time. It answers from your product's own
documentation, and it can also do the steps for them, asking approval before anything
that saves, sends, pays or deletes.

![The guide walking a user through inviting a teammate](../../docs/images/examples/in-app-guide.gif)

Unlike the other examples, this agent is for your customers, not your team. Every user
of your app gets their own agent profile on Omnara and their own conversation history,
and you can read every conversation step by step in the dashboard.

## What you need

- An Omnara account ([app.omnara.com](https://app.omnara.com)) and a project for the
  guide
- A web app you can add one component (or one script tag) and one server route to
- Node 24 or newer for the scripts

## Build it

Open Claude Code, Codex, Cursor, or any other coding agent in your app's repository and
say:

```text
Read https://raw.githubusercontent.com/omnara-ai/omnara/main/examples/in-app-guide/SKILL.md and follow it to add an in-app guide to this app.
```

Your coding agent follows [SKILL.md](SKILL.md). It reads your app first: how the front
end is built, how the server knows who is signed in, and where your product knowledge
is. It asks for the product name and whether the guide may act or only show. Then it
copies the code in, renders the agent config, and checks the guide on your app with
you. SKILL.md is plain steps if you'd rather do them yourself.

## How it works

```
your app
  <Guide />  ── fetch + SSE ──▶  /api/guide/*  (createGuideHandler, server only)
   │ snapshot of the page                         │ Omnara API key, who the user is
   │ runs show_step / click / fill / submit       ▼
   └──────────── tool results ◀──── Omnara agent (one per conversation)
                                      ├─ four custom tools, answered by the browser
                                      └─ your product knowledge
```

1. The user asks a question. The widget sends it with a text snapshot of the page:
   headings, visible text, table rows, and every control with a short ref (`[e12]`).
   Your app adds no markup for this; the snapshot reads standard HTML and ARIA.
2. The agent works out the steps from your product knowledge and calls one tool at a
   time. The tools are [custom tools](https://docs.omnara.com/tools/custom): Omnara
   holds each call until the widget in the user's browser runs it and posts the result,
   with a fresh snapshot.
   - **show_step** ("Show me"): move the cursor, highlight, and wait for the user.
   - **click** and **fill** ("Do it for me"): act for the user.
   - **submit**: the final save, send or pay. It is set to `always_ask`, so Omnara
     opens an approval that the panel shows before anything happens.
3. When the task is done, the agent replies with a short summary.

**One profile per user.** The server creates an agent profile for each user of your app
the first time they use the guide, named `<prefix> · <their name>`, from
`guide.agent.yaml` with their name and role written into the instruction. Each
conversation is an agent launched from that profile, so the panel's history is that
user's agents and nothing else is stored.

**The key stays on your server.** The browser only talks to your route. It carries a
session token signed by your server that names the user and the conversation, so a
browser can reach its own conversation and no one else's.

## Product knowledge

`guide.config.json` names where the agent learns your product. The page is always a
source too.

| Your product has | `knowledge.source` | How the agent reads it |
| --- | --- | --- |
| A docs site with an MCP server (Mintlify and similar) | `mcp` | The MCP server's search and read tools |
| A public docs site, no MCP | `web` | Omnara's `web_fetch`, with the page list in the instruction |
| Markdown files: a `docs/` folder, an exported wiki | `files` | An Omnara [memory store](https://docs.omnara.com/agents/configuration#memory-stores), searched and read with the built-in file tools |
| Nothing written down | `none` | The page alone |

With nothing written down, don't stop at `none`: SKILL.md has your coding agent write
short notes from your app's code and screens (what the product is, where everything is,
how the common tasks go) and use them as `files`. Read them before going live, because
the guide repeats what they say.

`pnpm run sync` uploads a `files` folder and keeps the store in step with it:
unchanged files are skipped and removed ones are deleted.

## Show only

Set `"do_mode": false` in `guide.config.json` and pass `showOnly` to the widget. The
agent then has no tools for clicking, typing or submitting, so it can only point. Start
here if your app moves money or handles regulated data.

## Try it on your app first

`try/proxy.ts` puts the guide on a running app without changing the app. It is a local
reverse proxy that passes every request to your app and adds the guide's script to each
page:

```sh
pnpm install
cp .env.example .env                       # the Omnara key and IDs, and APP_URL
cp guide.config.example.json guide.config.json
pnpm run render
pnpm run try                               # http://localhost:3200
```

Without `GUIDE_ME_PATH`, everyone who opens the proxy is one trial user. Local use
only.

## What it has been tested on

- **Omnara's own dashboard** (React, Radix dialogs and menus, a strict
  Content-Security-Policy), through `try/proxy.ts`: creating and revoking API keys,
  inviting members, adding secrets, creating projects, connecting Slack and a model
  provider, in both modes.
- **All four knowledge sources** against that dashboard, with the same questions. With
  `none`, walkthroughs still worked from the page and concept questions got "I can't
  tell from this page" instead of a guess.
- **One model**: `anthropic/claude-sonnet-5.5`. The first highlight took 3 to 8
  seconds and each later step about 3.

What it doesn't handle:

- Content inside iframes or drawn on a canvas, and custom code editors.
- The browser's own `confirm()` boxes: the user answers those; the guide can't see
  them.
- Very large pages: a snapshot lists the first 180 controls and 140 lines of text.
- Controls with no role or label, such as a clickable `<div>`. Give them a role and a
  name; that fixes the guide and your app's accessibility together.
- Component libraries other than Radix-style ones haven't been tried. Dialog focus
  traps that listen in the capture phase may still pull focus from the panel.
- "Do it for me" relies on the agent using `submit`, not `click`, for final actions.
  The instruction says so and Omnara then requires approval, but nothing on the page
  forces it. Use show only where that isn't good enough.

## Files

```
SKILL.md                    the steps your coding agent follows
agent.yaml                  the agent config template: instruction and the four tools
guide.config.example.json   product name, model, knowledge source, do_mode
guide/client/               <Guide />: panel, page snapshot, cursor overlay, tool runner
guide/server/               createGuideHandler(): the route that holds the Omnara key
guide/embed.tsx             the widget as one script, for apps that aren't React
scripts/render-agent.mjs    agent.yaml + guide.config.json → guide.agent.yaml
scripts/sync-knowledge.mjs  uploads a "files" folder to its memory store
scripts/build-embed.mjs     builds embed.js
try/proxy.ts                the guide on a running app, without changing the app
```

## Check

```sh
pnpm run typecheck
```
