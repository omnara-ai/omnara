import { Webhooks } from '@octokit/webhooks'
import { bearerToken, createOmnaraClient, type OmnaraClient } from '@omnara/sdk'
import { Hono } from 'hono'
import { createMiddleware } from 'hono/factory'
import { HTTPException } from 'hono/http-exception'
import { jwtVerify } from 'jose'
import { z } from 'zod'
import { type Bindings, zBindings } from './env'
import { handlePullRequest } from './review'
import { postReviewComment, zReviewCommentRequest } from './comment'
import { REVIEW_TOKEN_ISSUER, type ReviewClaims, zReviewClaims } from './auth'

const zGitHubEventName = z.enum([
  'pull_request',
  'ping',
  'installation',
  'installation_repositories',
])

type WorkerEnv = {
  Bindings: Record<string, unknown>
  Variables: {
    env: Bindings
    omnara: OmnaraClient
  }
}

const loadEnv = createMiddleware<WorkerEnv>(async (c, next) => {
  const env = zBindings.parse(c.env)
  c.set('env', env)
  c.set(
    'omnara',
    createOmnaraClient({
      baseUrl: env.OMNARA_API_URL,
      auth: bearerToken(env.OMNARA_API_KEY),
    }),
  )
  await next()
})

const app = new Hono<WorkerEnv>()

app.onError((error, c) => {
  if (error instanceof HTTPException) return error.getResponse()
  console.error(error)
  return c.json({ error: error instanceof Error ? error.message : 'internal error' }, 500)
})

app.post('/webhooks/github', loadEnv, async (c) => {
  const { env, omnara } = c.var
  const eventName = zGitHubEventName.safeParse(c.req.header('x-github-event'))
  if (!eventName.success) return c.json({ ignored: c.req.header('x-github-event') }, 202)

  const webhooks = new Webhooks({ secret: env.GITHUB_WEBHOOK_SECRET })
  webhooks.on('pull_request', (event) => handlePullRequest(event, env, omnara))
  webhooks.onError(({ event, message }) => {
    console.error(`webhook ${event.name} failed: ${message}`)
  })

  try {
    await webhooks.verifyAndReceive({
      id: c.req.header('x-github-delivery') ?? crypto.randomUUID(),
      name: eventName.data,
      signature: c.req.header('x-hub-signature-256') ?? '',
      payload: await c.req.text(),
    })
  } catch (error) {
    if (error instanceof Error && /signature/i.test(error.message)) {
      throw new HTTPException(401, { message: 'invalid webhook signature' })
    }
    throw error
  }
  return c.json({ ok: true })
})

app.post('/review_comment', loadEnv, async (c) => {
  const { env } = c.var
  const token = c.req.header('authorization')?.match(/^Bearer\s+(\S+)$/i)?.[1]
  if (!token) throw new HTTPException(401, { message: 'missing bearer token' })
  let claims: ReviewClaims
  try {
    const { payload } = await jwtVerify(token, new TextEncoder().encode(env.REVIEW_JWT_SECRET), {
      issuer: REVIEW_TOKEN_ISSUER,
      algorithms: ['HS256'],
    })
    claims = zReviewClaims.parse(payload)
  } catch {
    throw new HTTPException(401, { message: 'invalid or expired review token' })
  }
  const request = zReviewCommentRequest.parse(await c.req.json())
  return c.json(await postReviewComment(env, claims, request), 201)
})

export default app
