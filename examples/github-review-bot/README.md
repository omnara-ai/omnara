# GitHub Review Bot

A GitHub App that reviews pull requests with an Omnara agent. PR events drive a state machine:
an eligible open/update event creates an agent or steers the existing one. The agent posts inline
comments, reviews, and issues through the worker. Closing the PR archives the
agent and deletes its token secrets.

## Files

- [src/worker.ts](src/worker.ts): HTTP routes, environment loading, and authentication.
- [src/review.ts](src/review.ts): state transitions, action dispatch, and agent/secret operations.
- [src/review-policy.ts](src/review-policy.ts): repository filters, review triggers, and ignore rules.
- [src/comment.ts](src/comment.ts): GitHub comments, reviews, and issues.
- [src/auth.ts](src/auth.ts): shared token claims and GitHub installation authentication.
- [src/env.ts](src/env.ts): validated worker bindings.

- [src/profile.ts](src/profile.ts): model, machine pool, tools,
  and machine overrides. `createReviewProfile()` fills in each PR's values.
  This is a local builder; no Omnara profile ID is needed.
- [src/prompt.ts](src/prompt.ts): agent instructions, the initial review message, and
  the follow-up message sent after each push.
- `.env`: worker URL, org/project IDs, API key, GitHub App credentials, and
  signing secrets. Start with [.env.example](.env.example).
- `sandboxStartupScript()` in `src/profile.ts`: includes the inline `comment.sh`
  helper in the machine startup override.
  That script installs `git`, `curl`, and `jq`, writes the helper to
  `/usr/local/bin/comment.sh`, and makes it executable on each new machine.
  It requires a Linux pool whose startup script runs as root.

## Install and log in

Run from the repository root. You need Node 22.18+, npm, pnpm (`pnpx`), and OpenSSL.

```sh
cd examples/github-review-bot
npm install
test -f .env || cp .env.example .env
chmod 600 .env

# CLI commands use your personal login; the worker key stays in .env.
unset OMNARA_API_KEY
pnpx omnara login
npx wrangler login
```

## When to review

Edit `reviewPolicy` in [src/review-policy.ts](src/review-policy.ts), then redeploy.
By default, automatic and requested reviews are enabled for all repositories
where the GitHub App is installed. Draft PRs, bot authors, and PRs with the
`skip-review` label are ignored.

For request-only reviews on selected repositories, change these fields:

```ts
repositories: ['my-org/api', 'my-org/web-*'],
excludedRepositories: ['my-org/web-legacy'],
autoReview: false,
reviewOnRequest: true,
requestedReviewers: ['reviewer-login'],
requestedTeams: ['my-org/code-review'],
```

- `repositories` is an allowlist; an empty list disables new review work.
  `excludedRepositories` takes precedence. This does not grant GitHub App access.
- `autoReview` handles opened, reopened, ready-for-review, synchronize, edited,
  and unlabeled events. Removing an ignore label makes an eligible PR reviewable.
- `reviewOnRequest` handles GitHub's **Request review** action (`review_requested`).
  User/team filters match the requested recipient, not the person making the
  request. If both lists are empty, any review request triggers a review.
- `ignore` supports `drafts`, `bots`, `authors`, `labels`, `baseBranches`, and
  `headBranches`. These filters apply to automatic and requested reviews alike.
- Patterns support `*` and match the entire value. Branch patterns are
  case-sensitive; repositories, authors, labels, and requested recipients are not.

With `autoReview: false`, subsequent pushes also wait for a new review request.
A review already running continues when a PR becomes ignored. Closing or merging
always cleans up an existing review, even after disabling reviews or excluding
its repository. Other notifications, such as assignments and removed review
requests, don't trigger a review.

## Find your org, project, model, and pool

```sh
pnpx omnara whoami --json
export OMNARA_ORG_ID='YOUR_ORG_ID'

pnpx omnara projects list --org "$OMNARA_ORG_ID" --json
export OMNARA_PROJECT_ID='YOUR_PROJECT_ID'

pnpx omnara grant models list --org "$OMNARA_ORG_ID" --project "$OMNARA_PROJECT_ID" --json
pnpx omnara grant pools list --org "$OMNARA_ORG_ID" --project "$OMNARA_PROJECT_ID" --json
```

Copy the org and project IDs into `.env`. In `src/profile.ts`, set
`model.provider_config`, `model.name`, and `machine_sources[0].machine_pool_name`
from the granted resources. These are names; the grant commands below use IDs.
Leave the startup override in place so the helper is installed automatically.

If the project doesn't have a suitable model or pool grant yet, list the org's
resources and grant the ones you want:

```sh
pnpx omnara model-providers list --org "$OMNARA_ORG_ID" --json
export REVIEW_MODEL_PROVIDER_ID='YOUR_MODEL_PROVIDER_CONFIG_ID'
pnpx omnara models list "$REVIEW_MODEL_PROVIDER_ID" --org "$OMNARA_ORG_ID" --json
pnpx omnara pools list --org "$OMNARA_ORG_ID" --json

export REVIEW_MODEL_ID='YOUR_CONFIGURED_MODEL_ID'
export REVIEW_POOL_ID='YOUR_MACHINE_POOL_ID'
pnpx omnara grant models add --org "$OMNARA_ORG_ID" --project "$OMNARA_PROJECT_ID" \
  --configured-model-id "$REVIEW_MODEL_ID"
pnpx omnara grant pools add --org "$OMNARA_ORG_ID" --project "$OMNARA_PROJECT_ID" \
  --machine-pool-id "$REVIEW_POOL_ID"
```

Only add missing grants. Machine startup overrides live in `src/profile.ts`;
you don't need to edit the pool grant's defaults.

Create a dedicated org API key in the Omnara console with org **member** and
**developer** access to this project, then set `OMNARA_API_KEY` in `.env`.
Org key creation requires a browser session and isn't available through the CLI.

## GitHub App and secrets

Deploy once to learn the worker URL:

```sh
npm run deploy
```

Set `PUBLIC_URL` in `.env` to that URL without a trailing slash. The worker's
health endpoint works before credentials are configured; webhook processing
requires the completed `.env`.

Generate two separate secrets and copy the output into `.env`:

```sh
printf 'GITHUB_WEBHOOK_SECRET=%s\n' "$(openssl rand -hex 32)"
printf 'REVIEW_JWT_SECRET=%s\n' "$(openssl rand -hex 32)"
```

In GitHub's personal or organization settings, create a GitHub App with:

| Setting | Value |
| --- | --- |
| Homepage URL | Your `PUBLIC_URL` |
| Webhook URL | Your `PUBLIC_URL` followed by `/webhooks/github` |
| Webhook secret | `GITHUB_WEBHOOK_SECRET` from `.env` |
| Contents permission | Read-only |
| Pull requests permission | Read and write |
| Issues permission | Read and write |
| Subscribed events | Pull request |

Metadata read access is automatic. Copy the **App ID** into `GITHUB_APP_ID`,
generate and download a private key, and install the app on the repositories
you want reviewed.

Format the downloaded PEM for `.env` with this command, replacing its path:

```sh
node --input-type=module - /path/to/github-app.pem <<'JS'
import { readFileSync } from 'node:fs'
const pem = readFileSync(process.argv[2], 'utf8').trim()
console.log(`GITHUB_APP_PRIVATE_KEY=${JSON.stringify(pem)}`)
JS
```

Copy the resulting line into `.env`. It contains the key with literal `\n`
between lines. No CLI login credential belongs in the worker configuration.

## Deploy and verify

Once `src/profile.ts` and `.env` are complete:

```sh
npm test
npm run typecheck
npx wrangler deploy --dry-run
npm run secrets
npm run deploy
```

Check health using the URL from `.env` and watch the worker logs:

```sh
node --env-file=.env --input-type=module -e \
  'const r = await fetch(process.env.PUBLIC_URL); console.log(r.status, await r.text()); if (!r.ok) process.exit(1)'
npx wrangler tail
```

Open a PR in a repository where the GitHub App is installed. In a
second terminal, inspect the agents (set the same org/project variables there):

```sh
pnpx omnara agents list --org "$OMNARA_ORG_ID" --project "$OMNARA_PROJECT_ID" --json
```

GitHub's App settings also show webhook deliveries and allow redelivery after
fixing configuration. Redeploy after editing `src/profile.ts`, `src/prompt.ts`, or `src/review-policy.ts`. Rerun
`npm run secrets` after changing `.env`.

## Local development

```sh
npm run dev
```

In another terminal, with `cloudflared` installed:

```sh
cloudflared tunnel --url http://localhost:8787
```

Set `PUBLIC_URL` in `.env` to the tunnel's HTTPS URL and change the GitHub App's
webhook URL to that URL plus `/webhooks/github`. Restart `npm run dev` after
changing `.env`. Restore both URLs before returning to the deployed worker.

## Editing the helper

Edit the `commentScript` template inside `sandboxStartupScript()` in
`src/profile.ts`, then redeploy. Both shell templates have `/* sh */` markers.
For shell syntax highlighting in VS Code, install
[Comment tagged templates](https://marketplace.visualstudio.com/items?itemName=bierner.comment-tagged-templates):

```sh
code --install-extension bierner.comment-tagged-templates
```

Inside these TypeScript templates, escape shell `${...}` as `\${...}` and
literal backslashes as `\\`.

```sh
npm run typecheck
npm run deploy
```

## Review state transitions

| Event | No active agent | Active agent |
| --- | --- | --- |
| Eligible automatic update or matching review request (`updated`) | Create an agent with the initial review request | Refresh tokens and send steering input |
| Any event for a closed or merged PR (`closed`) | Do nothing | Delete its token secrets, then archive the agent |

Cleanup runs before archiving so a failed deletion can be retried while the
agent remains active. Ignored open-PR events stop before making API calls. Eligible events and cleanup
events look up the agent once. The existence of an active Omnara agent
is the persisted state; there is no separate in-memory state to lose between
worker requests. Input and launch idempotency keys use the GitHub delivery
ID so separate updates at the same commit can be processed.

## Endpoints

| Route | Authentication | Purpose |
| --- | --- | --- |
| `POST /webhooks/github` | GitHub webhook signature | Launch, update, or archive the PR's agent |
| `POST /review_comment` | PR-scoped JWT | Post a comment, inline comment, review, or issue |

The sandbox gets a read-only GitHub installation token and a PR-scoped JWT
through project secrets. GitHub App credentials remain in the worker.
Installation tokens last one hour; each push refreshes the token. Agents are
named `github-review owner/repo#N`, and review eligibility is controlled by `src/review-policy.ts`.
