---
name: in-app-guide
description: Add an in-app guide to the user's web app, built on Omnara. The guide is a chat panel with an on-screen cursor that walks the app's users through tasks on the real page, answers from the product's own knowledge, and can do the steps for them with approval. Use when the user asks to add, set up, update, or remove an in-app guide, product tour agent, or "show me how" assistant in their app.
---

# Add an in-app guide to a web app

You are adding a guide to the user's own web app. When you finish, signed-in users of
that app have an "Ask …" button. They ask how to do something, and a cursor moves to the
right control on the page they are on, highlights it, and tells them what to do, one
step at a time. The agent behind it runs on Omnara. Each user of the app gets their own
agent profile and their own conversation history.

There are two halves, and both come from this folder:

- **The agent**: `agent.yaml`, a template you render with the product's name, a model,
  and where the product knowledge comes from.
- **The code in the app**: `guide/client` (the widget) and `guide/server` (one route
  that holds the Omnara API key). The agent's four tools have no code on Omnara's side;
  the widget runs them in the user's browser.

Read the app before you ask anything, then interview, then build. Don't create anything
in Omnara and don't change the app until the user has confirmed your summary in step 3.
When a step says to ask the user, ask in plain language, then end your turn and wait.
Where a step gives a default, use it without asking and say in a line what you picked.

Never ask the user to paste an API key into the chat. Keys go into the app's server
environment file, which the user edits.

The commands use the Omnara CLI (`npx omnara`); add `--json` when you need to read IDs.
If a command fails, `npx omnara <command> --help` and
[docs.omnara.com](https://docs.omnara.com) have the details.

## 1. Connect to Omnara

Run `npx omnara whoami`. If you aren't logged in, run `npx omnara login` without piping
its output, and keep it running: it waits until the user approves. It opens the approval
page in the user's browser and prints the page's link and a code. Show the user both
right away, and ask them to approve once the code matches. If login says the account has
no organization yet, have the user create one at the link it prints.

Pick the org and project (`npx omnara whoami --json` lists orgs,
`npx omnara projects list --org <org-id> --json` their projects). A guide for the
user's customers deserves its own project: if there isn't one, ask whether to create one
named after the product (`npx omnara projects create --help`) or use an existing one.
Save the choice with `npx omnara config --org <org-id> --project <project-id>`.

## 2. Read the app

Work out these things from the code before the interview, so you only ask what you
can't find:

1. **How the front end is built.** React 19: the widget is mounted as a component
   (React 18 should work but isn't tested). Anything else (Vue, Svelte,
   server-rendered pages): the widget is loaded as one script. Find the layout that
   every signed-in page shares.
2. **How the server answers requests.** The guide's route takes a web-standard
   `Request` and returns a `Response`, which Next.js route handlers, React Router and
   Remix loaders, Hono, SvelteKit, Bun and Deno accept as is. Express and Fastify need
   the small adapter shown in `try/proxy.ts` (`serveGuide`).
3. **How the server knows who is signed in**, and what it knows about them: a stable
   user ID, a display name, a role, the workspace or account they are in.
4. **Where the product knowledge is.** Look for a docs site (and whether it serves
   `/llms.txt` or an MCP endpoint at `/mcp`), a `docs/` folder, help-center exports, or
   nothing.
5. **Which actions can't be undone or cost money**: payments, deletions, sending
   messages, changing access.

## 3. Interview the user

Tell the user what you found, then ask for what is still missing, in one message:

- **Product name**, as users know it. The button reads "Ask <name>".
- **Product knowledge.** Say what you found in step 2 and which source you plan to use
  (step 5 lists the four). If you found nothing, say that you will write short notes
  from the code and screens, and that they should read them before going live.
- **Show only, or also do.** In "Show me" the guide points and the user clicks. With
  "Do it for me" also on, the guide can click and fill in forms itself, and asks the
  user to approve before anything that saves, sends, pays or deletes. Recommend show
  only when the app moves money or handles regulated data, and for a first rollout.
- **Who gets it.** Every signed-in user, or only some roles or accounts.

Then summarize the plan in a few lines (product name, knowledge source, show only or
not, which Omnara project, which files you will add to the app) and wait for a yes.

## 4. Get the example code

Fetch this folder if it isn't on disk:

```sh
git clone --depth 1 --filter=blob:none --sparse https://github.com/omnara-ai/omnara.git /tmp/omnara-example
git -C /tmp/omnara-example sparse-checkout set examples/in-app-guide
```

Copy it into the user's repository in two places:

- `guide/` goes where the app's own source lives, so the app's bundler compiles it, for
  example `src/guide/`. It has no dependencies beyond React.
- `agent.yaml`, `scripts/`, `try/`, `package.json`, `tsconfig.json`, `.env.example` and
  `guide.config.example.json` go in a folder of their own at the repository root, for
  example `in-app-guide/`. If you moved `guide/` elsewhere, fix the two import paths
  that point at it: `scripts/build-embed.mjs` and `try/proxy.ts`.

Run `pnpm install` (or `npm install`) in that folder. The scripts need Node 24 or newer.

## 5. Product knowledge

Copy `guide.config.example.json` to `guide.config.json` and set `product`. Then set
`knowledge` to one of these. The page the user is on is always a source as well.

| The product has | `knowledge` |
| --- | --- |
| A docs site with an MCP server (Mintlify and similar serve one at `/mcp`) | `{ "source": "mcp", "mcp_url": "…/mcp", "index_url": "…/llms.txt", "base_url": "https://docs.…" }` |
| A public docs site without MCP | `{ "source": "web", "index_url": "…/llms.txt", "base_url": "https://docs.…" }` |
| Markdown files: a `docs/` folder, an exported wiki or help center | `{ "source": "files", "dir": "../docs", "store": "<product>-guide-docs" }` |
| Nothing written down | write notes (below), then use them as `files` |

Details:

- **mcp**: check the endpoint answers before relying on it. `index_url` is optional; it
  inlines the list of pages into the instruction so the agent knows what exists. If the
  server has tools the guide shouldn't call, list them in `"disable_tools"`.
- **web**: `index_url` must be a list of pages in the `llms.txt` format (lines like
  `- [Title](https://…): description`). If the site has none, write one by hand next to
  the config and serve it, or use `files` instead.
- **files**: `dir` is relative to `guide.config.json`. Use `"exclude"` for folders that
  aren't for end users (API reference, changelogs, internal runbooks). Add `base_url`
  only if the same files are published at `base_url` + path without the extension;
  otherwise leave it out and the agent names places in the app instead of linking.
  Markdown and MDX both work. Files are uploaded in step 6.
- **`"source": "none"`** exists: the agent then works from the page alone. It still
  finds its way around by reading the screen, and it says so when it doesn't know. Use
  it only as a stopgap.

### Writing notes when there are no docs

Write three short markdown files into `in-app-guide/notes/`, from the app's code and,
if you can run the app, its screens. Use the exact words that are on the screen.

- `overview.md`: what the product is in a paragraph, its main objects and how they
  relate, the roles and what each can do, and what can't be undone.
- `navigation.md`: every item in the navigation, its address, and the main buttons on
  that page.
- `tasks.md`: a numbered step list for each common task, with exact button names. Mark
  the step that saves, sends, pays or deletes as final.

Give each file front matter with a `title` and a one-line `description`; the agent sees
those in its index. State at the top that these are notes written from the code, not
official documentation. Leave out secrets, customer data, and anything about internals
that an end user shouldn't be told. Keep each file under about 10 KB. Then set
`"knowledge": { "source": "files", "dir": "notes", "store": "<product>-guide-notes" }`
and ask the user to read the notes before going live: the guide will repeat what they
say.

## 6. Pick the model, render the config, upload the files

1. `npx omnara grant models list --json`: each item has `model.provider_config` and
   `model.name`. Use `anthropic/claude-sonnet-5.5` on `omnara-openrouter` if it's
   granted (the guide was tested with it), otherwise the strongest model on the list
   that is good at tool use. Set `model_provider` and `model_name` in
   `guide.config.json`.
2. Show only: set `"do_mode": false`. That removes the agent's tools for clicking,
   typing and submitting, so it can't act even if asked to.
3. `pnpm run render` writes `guide.agent.yaml`. Read it once: the instruction should
   name the product and the knowledge source you chose, and
   `grep -n '{{' guide.agent.yaml` should print only the `{{user}}` line, which the
   server fills in per user.
4. For `files`: after step 7, `pnpm run sync` uploads the folder to an Omnara memory
   store. Run it again whenever the files change; put it in CI if the docs change often.

## 7. The API key

The app's server needs an organization API key, which the user creates:

1. Ask the user to open the Omnara dashboard, choose **All projects** in the project
   switcher, open **API Tokens**, click **New token**, and create one with the
   **Member** role.
2. Then, from the key's detail panel, grant it the **developer** role on the project
   from step 1 only. The guide creates an agent profile per user and launches agents,
   which needs that role; it needs nothing outside that project.
3. Ask them to put the key in the app's server environment (and in `in-app-guide/.env`
   for the scripts) as `OMNARA_API_KEY`, with `OMNARA_ORG_ID` and `OMNARA_PROJECT_ID`.
   Give them the two IDs. Check that both files are ignored by git.

## 8. Try it before changing the app (optional)

`try/proxy.ts` puts the guide on the running app without touching its code: it passes
every request through to the app and adds the guide's script to each page.

In `in-app-guide/.env` set `APP_URL` to where the app runs locally, `GUIDE_TITLE`, and
`GUIDE_ME_PATH` if the app has an endpoint that returns the signed-in user as JSON.
Start the app, run `pnpm run try`, and have the user open http://localhost:3200, sign
in, and ask the guide a real question. This is the quickest way to find out whether the
guide reads this app's pages well, and it is worth doing before step 9.

## 9. Add the route

Create a catch-all route at `/api/guide/*` (GET, POST and OPTIONS) that calls
`createGuideHandler` from `guide/server`:

```ts
import { readFileSync } from 'node:fs'
import { createGuideHandler } from '@/guide/server'

export const { GET, POST, OPTIONS } = createGuideHandler({
  token: process.env.OMNARA_API_KEY!,
  orgId: process.env.OMNARA_ORG_ID!,
  projectId: process.env.OMNARA_PROJECT_ID!,
  config: readFileSync('in-app-guide/guide.agent.yaml', 'utf8'),
  profilePrefix: 'Acme guide',
  sessionSecret: process.env.GUIDE_SESSION_SECRET,
  async getUser(req) {
    const user = await currentUser(req) // the app's own session lookup
    if (!user) return null
    return { id: user.id, displayName: user.name, context: `${user.name}, ${user.role} at ${user.workspaceName}.` }
  },
})
```

- `getUser` is the only part you write. Return `null` for anyone who shouldn't get the
  guide. `id` must be stable; `context` is one or two lines the agent is told about this
  user, so put the role there. Never trust a user ID sent by the browser.
- The config file must be readable where the server runs. If the deployment doesn't
  ship loose files, import the YAML as a string at build time instead.
- Set `GUIDE_SESSION_SECRET` to a random value in production.
- The route streams (server-sent events). Turn off response buffering for it if the
  host buffers by default, and don't cache it.

## 10. Add the widget

React, in the layout that signed-in pages share:

```tsx
import { Guide } from '@/guide/client'

<Guide endpoint="/api/guide" title="Ask Acme" suggestions={['How do I invite a teammate?']} />
```

Add `showOnly` if you set `"do_mode": false`. Use the app's accent color for `accent`.
If a support chat bubble already sits in the bottom-right corner, move the guide with
`placement="bottom-left"` or `offset={{ side: 80 }}`. To open it from the app's own
Help menu, call `openGuide()` from `guide/client`.

Not React: run `pnpm run build:embed`, serve `embed.js` from the app's own origin, and
add to the signed-in layout:

```html
<script src="/embed.js" data-endpoint="/api/guide" data-title="Ask Acme"></script>
```

If the app sends a Content-Security-Policy, the script and the route are same-origin,
so `script-src 'self'` and `connect-src 'self'` already allow them. The widget's styles
live in a `<style>` element inside its own shadow root, which needs `style-src` to
allow inline styles.

## 11. Check it

Run the app, sign in, and with the user watching:

1. Ask a question that needs no clicks. The answer should come from the product
   knowledge, and say so when it doesn't know.
2. Ask how to do one common task and follow the highlights to the end. Include a task
   that opens a dialog and one that uses a dropdown, since those are where apps differ.
3. If "Do it for me" is on, ask it to do a harmless task. It must stop and show an
   approval card before the final save.
4. Sign in as a user who shouldn't be able to do something, and ask for it. The guide
   should say they can't, not walk them into a wall.

In the Omnara dashboard, the project now has a profile named
"<profilePrefix> · <user's name>" and one agent per conversation; open one to read the
conversation step by step. If a step points at the wrong control or can't see one, look
at how that control is built: the guide reads standard HTML and ARIA, so a clickable
`<div>` with no role or label is invisible to it. Fix the markup; the app's
accessibility improves too.

## 12. Wrap up

Summarize what you added: the files in the app, the route, the widget, the Omnara
project, the memory store if any, and that the user holds the API key. Then tell them:

- **Change what the guide knows or how it behaves:** edit the docs or notes and run
  `pnpm run sync`, or edit `guide.config.json` / `agent.yaml` and run `pnpm run render`,
  then redeploy. Each user's profile picks up the new config on their next conversation.
- **Read conversations:** each one is an agent in the Omnara project, with every step
  and tool call.
- **Remove it:** take out the widget and the route, revoke the API key in the
  dashboard, and delete the profiles and the memory store in the project.
- **What it doesn't handle:** content inside iframes or drawn on a canvas, the
  browser's own confirm boxes, and very large pages, where it reads the first 180
  controls. README.md lists what it was tested on.
