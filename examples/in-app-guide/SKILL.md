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

- **The agent**: `agent.yaml`, a template you fill in with the product's name, a model,
  and where the product knowledge comes from.
- **The code in the app**: `guide/`, which has the widget (`Guide.tsx`) and one server
  route that holds the Omnara API key (`server.ts`). The agent's four tools have no
  code on Omnara's side; the widget runs them in the user's browser.

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
   Remix loaders, Hono, SvelteKit, Bun and Deno accept as is. For Express or Fastify,
   build a `Request` from the incoming request and pipe the `Response` back, without
   buffering the body.
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
  (step 5 lists them). If you found nothing, say that you will write short notes
  from the code and screens, and that they should read them before going live.
- **Show only, or also do.** In "Show me" the guide points and the user clicks. With
  "Do it for me" also on, the guide can click and fill in forms itself, and asks the
  user to approve before anything that saves, sends, pays or deletes. Recommend show
  only when the app moves money or handles regulated data, and for a first rollout.
- **Who gets it.** Every signed-in user, or only some roles or accounts.

Then summarize the plan in a few lines (product name, knowledge source, show only or
not, which Omnara project, which files you will add to the app) and wait for a yes.

## 4. Get the example files

If this folder isn't on disk, fetch it:

```sh
git clone --depth 1 --filter=blob:none --sparse https://github.com/omnara-ai/omnara.git /tmp/omnara-example
git -C /tmp/omnara-example sparse-checkout set examples/in-app-guide
```

Copy `guide/` to where the app's own source lives, so the app's bundler compiles it,
for example `src/guide/`. It depends on nothing but React. Copy `agent.yaml` to
`guide.agent.yaml` at the root of the user's repository; that copy is the one you edit
and the server reads.

## 5. Product knowledge

The agent learns the product from one source, plus the page the user is on. Pick the
source, then fill in three placeholders in `guide.agent.yaml` and keep the matching
block at the end of the file, deleting the other knowledge blocks.

| The product has | Keep | `{{KNOWLEDGE}}` |
| --- | --- | --- |
| A docs site with an MCP server (Mintlify and similar serve one at `/mcp`) | `mcp:` with `{{DOCS_MCP_URL}}` | `Search the <product> docs with the docs tools unless the page itself makes the path obvious. The docs index below tells you which page to read.` |
| A public docs site without MCP | `web_fetch` under `tools:` | `Read the <product> docs with web_fetch unless the page itself makes the path obvious. The docs index below lists every page: pick the one that fits and fetch its URL. Only fetch URLs from that index.` |
| Markdown files: a `docs/` folder, an exported wiki or help center | `memory_stores:` with `{{MEMORY_STORE}}` | `What is written down about <product> is in files under /memory/<store>/. Unless the page itself makes the path obvious, pick the file from the index below and read it with storage_read, or search all of them with storage_search (path "/memory/<store>/**", args like ["-i", "-e", "invite"]). Read only the files you need.` |
| Nothing written down | write notes (below), then treat them as markdown files | as for markdown files |

- **`{{KNOWLEDGE_INDEX}}`** is the list of what there is to read, one line each, under a
  heading `Docs index:` or `Files:`. For a docs site, take the page lines from its
  `/llms.txt` (`- [Title](URL): description`) and leave out API reference pages. For
  files, write `- /memory/<store>/<path>: <title>. <one-line description>` per file.
- **`{{LINKS}}`**: if there is a public docs site, `When a doc page backs up what you
  say, link it as a markdown link with the full URL (it must start with <docs URL>);
  never link to a path inside the app.` If there isn't, `There is no public
  documentation to link to, so don't add links; name the place in the app instead.`
- **Markdown files** go into a memory store that agents can only read. Leave out
  folders that aren't for end users (API reference, changelogs, internal runbooks):

  ```sh
  npx omnara memory-stores create --name <product>-guide-docs --agent-access read --json
  # for each file, with --path relative to the docs folder:
  npx omnara memory-stores files upload <memory-store-id> --path getting-started.md --file docs/getting-started.md
  ```

  Upload again when a file changes. Replacing a file needs
  `--expected-digest <digest>`: each upload prints the file's digest, and an upload
  without it fails with the current digest in the error.
- **An MCP server** may have tools the guide shouldn't call, such as feedback forms.
  Turn each off under `mcp.docs` with `tools: { <tool-name>: { enabled: false } }`.

If you can't set any of this up yet, delete all three knowledge blocks and use
`Nothing is written down about <product> for you to read. Work from what the page
shows: its navigation, headings, labels and controls. Explore one step at a time, and
when you can't tell how something is done, say so instead of guessing.` The guide still
finds its way around by reading the screen, and says when it doesn't know. Treat that
as a stopgap.

### Writing notes when there are no docs

Write three short markdown files from the app's code and, if you can run the app, its
screens. Use the exact words that are on the screen.

- `overview.md`: what the product is in a paragraph, its main objects and how they
  relate, the roles and what each can do, and what can't be undone.
- `navigation.md`: every item in the navigation, its address, and the main buttons on
  that page.
- `tasks.md`: a numbered step list for each common task, with exact button names. Mark
  the step that saves, sends, pays or deletes as final.

State at the top of each that these are notes written from the code, not official
documentation. Leave out secrets, customer data, and internals an end user shouldn't be
told. Keep each file under about 10 KB. Commit them to the user's repository, upload
them as markdown files, and ask the user to read them before going live: the guide
repeats what they say.

## 6. Finish the agent config

1. `npx omnara grant models list --json`: each item has `model.provider_config` and
   `model.name`. Use `anthropic/claude-sonnet-5.5` on `omnara-openrouter` if it's
   granted (the guide was tested with it), otherwise the strongest model on the list
   that is good at tool use. These are `{{MODEL_PROVIDER_CONFIG}}` and `{{MODEL_NAME}}`.
2. Replace `{{PRODUCT}}` everywhere.
3. Show only: delete the tools between the two `do mode` comments, and replace rule 4
   of the instruction with `You only show; you never act on the page yourself. If the
   user asks you to do something for them, say that you can walk them through it, and
   do that.` The agent then has no way to click, type or submit.
4. Check it: `grep -n '{{' guide.agent.yaml` must print only the `{{user}}` line, which
   the server fills in for each user.

## 7. The API key

The app's server needs an organization API key, which the user creates:

1. Ask the user to open the Omnara dashboard, choose **All projects** in the project
   switcher, open **API Tokens**, click **New token**, and create one with the
   **Member** role.
2. Then, from the key's detail panel, grant it the **developer** role on the project
   from step 1 only. The guide creates an agent profile per user and launches agents,
   which needs that role; it needs nothing outside that project.
3. Ask them to put the key in the app's server environment as `OMNARA_API_KEY`, with
   `OMNARA_ORG_ID` and `OMNARA_PROJECT_ID`. Give them the two IDs, and check that the
   environment file is ignored by git.

## 8. Add the route

Create a catch-all route at `/api/guide/*` (GET, POST and OPTIONS) that calls
`createGuideHandler` from `guide/server`:

```ts
import { readFileSync } from 'node:fs'
import { createGuideHandler } from '@/guide/server'

export const { GET, POST, OPTIONS } = createGuideHandler({
  token: process.env.OMNARA_API_KEY!,
  orgId: process.env.OMNARA_ORG_ID!,
  projectId: process.env.OMNARA_PROJECT_ID!,
  config: readFileSync('guide.agent.yaml', 'utf8'),
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

## 9. Add the widget

React, in the layout that signed-in pages share:

```tsx
import { Guide } from '@/guide/Guide'

<Guide endpoint="/api/guide" title="Ask Acme" suggestions={['How do I invite a teammate?']} />
```

Add `showOnly` if you made the guide show only in step 6. Use the app's accent color
for `accent`.
If a support chat bubble already sits in the bottom-right corner, move the guide with
`placement="bottom-left"` or `offset={{ side: 80 }}`. To open it from the app's own
Help menu, call `openGuide()` from `guide/Guide`.

Not React: bundle `guide/embed.tsx` into one script, which brings its own React
(`npm install --save-dev react react-dom esbuild`, then
`npx esbuild src/guide/embed.tsx --bundle --minify --outfile=public/guide.js`), serve
it from the app's own origin, and add to the signed-in layout:

```html
<script src="/guide.js" data-endpoint="/api/guide" data-title="Ask Acme"></script>
```

If the app sends a Content-Security-Policy, the script and the route are same-origin,
so `script-src 'self'` and `connect-src 'self'` already allow them. The widget's styles
live in a `<style>` element inside its own shadow root, which needs `style-src` to
allow inline styles.

## 10. Check it

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
at how that control is built: the guide reads standard HTML and ARIA, so it can't
name an icon-only button with no label, and it misses a clickable element that has
neither a role nor a pointer cursor. Fix the markup; the app's accessibility improves
too.

## 11. Wrap up

Summarize what you added: the files in the app, the route, the widget, the Omnara
project, the memory store if any, and that the user holds the API key. Then tell them:

- **Change what the guide knows or how it behaves:** upload the changed docs or notes
  again, or edit `guide.agent.yaml` and redeploy. Each user's profile picks up the new
  config on their next conversation.
- **Read conversations:** each one is an agent in the Omnara project, with every step
  and tool call.
- **Remove it:** take out the widget and the route, revoke the API key in the
  dashboard, and delete the profiles and the memory store in the project.
- **What it doesn't handle:** content inside iframes or drawn on a canvas, the
  browser's own confirm boxes, and very large pages, where it reads the first 180
  controls. README.md lists what it was tested on.
