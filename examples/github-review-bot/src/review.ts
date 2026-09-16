import type { EmitterWebhookEvent } from '@octokit/webhooks'
import { type OmnaraClient, sdk } from '@omnara/sdk'
import { SignJWT } from 'jose'
import type { Bindings } from './env'
import { installationToken, REVIEW_TOKEN_ISSUER, REVIEW_TOKEN_TTL, type ReviewClaims } from './auth'
import { createReviewProfile } from './profile'
import { createReviewMessage, createUpdateMessage } from './prompt'
import { getReviewEvent } from './review-policy'

// Omnara's active agent is the persisted state; rehydrate it for each webhook.
export const reviewMachine = {
  absent: {
    updated: { target: 'active', action: 'create' },
    closed: { target: 'absent', action: 'ignore' },
  },
  active: {
    updated: { target: 'active', action: 'steer' },
    closed: { target: 'absent', action: 'archive' },
  },
} as const

export async function handlePullRequest(
  { id, payload }: EmitterWebhookEvent<'pull_request'>,
  env: Bindings,
  omnara: OmnaraClient,
): Promise<void> {
  const event = getReviewEvent(payload)
  if (event === 'ignored') return

  const pr = payload.pull_request
  const context: ReviewContext = {
    env,
    omnara,
    scope: { orgID: env.OMNARA_ORG_ID, projectID: env.OMNARA_PROJECT_ID },
    payload,
    deliveryId: id,
    name: `github-review ${payload.repository.full_name}#${pr.number}`,
  }
  const agent = await findReviewAgent(context)
  const transition = agent ? { ...reviewMachine.active[event], agent } : reviewMachine.absent[event]

  switch (transition.action) {
    case 'ignore':
      return
    case 'create':
      await createReview(context)
      break
    case 'steer':
      await steerReview(context, transition.agent.id)
      break
    case 'archive':
      await archiveReview(context, transition.agent.id)
      break
  }
  console.log(
    `${context.name}: ${agent ? 'active' : 'absent'} -> ${transition.target} (${transition.action})`,
  )
}

interface ReviewContext {
  env: Bindings
  omnara: OmnaraClient
  scope: { orgID: string; projectID: string }
  payload: EmitterWebhookEvent<'pull_request'>['payload']
  deliveryId: string
  name: string
}

async function findReviewAgent({ omnara, scope, name }: ReviewContext) {
  const { data } = await sdk.listAgents({
    client: omnara,
    path: scope,
    query: { name: name.replace(/[*?\\]/g, (char) => `\\${char}`), limit: 5 },
  })
  return data.data.find((agent) => agent.name === name && agent.state === 'active')
}

async function createReview(context: ReviewContext): Promise<void> {
  const { env, omnara, scope, payload, deliveryId, name } = context
  const secrets = await refreshReviewSecrets(context)
  const pr = payload.pull_request
  const profile = createReviewProfile({
    publicUrl: env.PUBLIC_URL,
    repo: payload.repository.full_name,
    prNumber: pr.number,
    baseRef: pr.base.ref,
    headRef: pr.head.ref,
    ...secrets,
  })
  const { data: config } = await sdk.createAgentConfig({
    client: omnara,
    path: scope,
    body: { source: JSON.stringify(profile), source_format: 'json' },
  })
  await sdk.createAgent({
    client: omnara,
    path: scope,
    headers: { 'Idempotency-Key': `github-review-${deliveryId}` },
    body: { config: config.id, name, message: createReviewMessage(payload) },
  })
}

async function steerReview(context: ReviewContext, agentID: string): Promise<void> {
  const { omnara, scope, payload, deliveryId } = context
  await refreshReviewSecrets(context)
  await sdk.createAgentInput({
    client: omnara,
    path: { ...scope, agentID },
    headers: { 'Idempotency-Key': `github-review-${deliveryId}` },
    body: {
      content_blocks: [{ type: 'text', text: createUpdateMessage(payload) }],
      delivery_mode: 'steering',
      actor: payload.sender
        ? {
            provider_tenant_id: payload.repository.full_name,
            provider_user_id: String(payload.sender.id),
            display_name: payload.sender.login,
          }
        : undefined,
    },
  })
}

async function archiveReview(context: ReviewContext, agentID: string): Promise<void> {
  const { omnara, scope, payload } = context
  const secrets = await listSandboxSecrets(
    omnara,
    scope,
    payload.repository.full_name,
    payload.pull_request.number,
  )
  for (const secret of secrets) {
    await sdk.deleteSecret({ client: omnara, path: { orgID: scope.orgID, secretID: secret.id } })
  }
  // Keep the agent active until cleanup succeeds so a failed deletion can retry.
  await sdk.archiveAgent({ client: omnara, path: { ...scope, agentID } })
}

async function refreshReviewSecrets({ env, omnara, scope, payload }: ReviewContext) {
  const installationScope = 'installation' in payload ? payload.installation : undefined
  if (!installationScope)
    throw new Error('webhook payload has no installation; is this a GitHub App webhook?')
  const claims = {
    repo: payload.repository.full_name,
    pr: payload.pull_request.number,
    installation_id: installationScope.id,
  }
  const [installation, reviewToken] = await Promise.all([
    installationToken(env, claims, { contents: 'read', pull_requests: 'read' }),
    new SignJWT(claims)
      .setProtectedHeader({ alg: 'HS256' })
      .setIssuer(REVIEW_TOKEN_ISSUER)
      .setSubject(`${claims.repo}#${claims.pr}`)
      .setIssuedAt()
      .setExpirationTime(REVIEW_TOKEN_TTL)
      .sign(new TextEncoder().encode(env.REVIEW_JWT_SECRET)),
  ])
  const existing = await listSandboxSecrets(omnara, scope, claims.repo, claims.pr)
  const [githubTokenSecretId, reviewTokenSecretId] = await Promise.all([
    upsertSecret(omnara, scope, existing, claims, 'github-token', installation.token),
    upsertSecret(omnara, scope, existing, claims, 'review-token', reviewToken),
  ])
  return { githubTokenSecretId, reviewTokenSecretId }
}

const SECRET_METADATA_BOT = 'github-review-bot'

async function upsertSecret(
  omnara: OmnaraClient,
  scope: { orgID: string; projectID: string },
  existing: Array<{ id: string; metadata: Record<string, string> }>,
  claims: ReviewClaims,
  purpose: 'github-token' | 'review-token',
  value: string,
): Promise<string> {
  const material = { kind: 'generic' as const, value }
  const current = existing.find((secret) => secret.metadata.purpose === purpose)
  if (current) {
    await sdk.createSecretVersion({
      client: omnara,
      path: { orgID: scope.orgID, secretID: current.id },
      body: { material },
    })
    return current.id
  }
  const { data: secret } = await sdk.createSecret({
    client: omnara,
    path: { orgID: scope.orgID },
    body: {
      owner: { kind: 'project', project_id: scope.projectID },
      name: `github-review ${claims.repo}#${claims.pr} ${purpose}`,
      metadata: { bot: SECRET_METADATA_BOT, repo: claims.repo, pr: String(claims.pr), purpose },
      material,
    },
  })
  return secret.id
}

async function listSandboxSecrets(
  omnara: OmnaraClient,
  scope: { orgID: string; projectID: string },
  repo: string,
  pr: number,
) {
  const { data } = await sdk.listSecrets({
    client: omnara,
    path: { orgID: scope.orgID },
    query: {
      owner_kind: 'project',
      owner_project_id: scope.projectID,
      metadata: { bot: SECRET_METADATA_BOT, repo, pr: String(pr) },
    },
  })
  return data.data
}
