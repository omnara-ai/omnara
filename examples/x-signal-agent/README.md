# X Signal Agent

An agent that listens to X (Twitter) for you. Once a day it searches the last
24 hours of conversation about any topic you pick, filters out the noise, and
delivers a short digest — at most five posts, each with why it matters and a
suggested action. Reply in Slack (or the Omnara console) to ask for reply
drafts or push back on the filtering. It never posts to X.

The whole example is one notebook, [demo.ipynb](demo.ipynb). The agent itself
is a single config object — there is no service to deploy. Run the cells top
to bottom and you have your own.

## What you need

- An Omnara account ([app.omnara.com](https://app.omnara.com)) with an API key
- An X API bearer token with pay-per-use billing
  ([console.x.com](https://console.x.com)) — a daily scan costs on the order
  of cents
- [Deno](https://docs.deno.com/runtime/getting_started/installation/)

## Run it

```sh
brew install deno
deno jupyter --install    # register the Deno kernel with Jupyter

cd examples/x-signal-agent
cp .env.example .env      # set OMNARA_API_KEY and X_BEARER_TOKEN
deno install              # fetch @omnara/sdk into node_modules
```

Deno is only needed to run the notebook (it provides the TypeScript Jupyter
kernel; `deno install` fetches `@omnara/sdk` from `package.json`). The SDK itself works
on Node and Bun too — you can copy the cells into a plain script if you
prefer.

Open `demo.ipynb` (VS Code, Cursor, or `jupyter lab`), pick the **Deno**
kernel, and run top to bottom. One cell holds the topic and the X search
query — edit those two lines to point the agent at whatever you want to
track. Every cell is idempotent: rerun after editing and the agent updates
in place.

## Slack and scheduling (optional)

The notebook can create a Slack app for you. Invite the bot to a channel and
mention it — the agent delivers its digest in that thread, and thread replies
become instructions to the agent. There is no default channel: it answers
wherever it is mentioned or DM'd.

The last cell schedules a weekday-morning scan. Scheduled runs launched from
the profile deliver to the Omnara console; to get them in a Slack channel,
mention the bot there once and point the trigger at that agent instead.
